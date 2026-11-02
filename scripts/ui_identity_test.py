#!/usr/bin/env python3
"""Owner + two Members in real Chromium with unmodified CSP/HTTPS API.
No fabricated backend or local-browser policy changes. A managed-browser
navigation refusal is reported as BLOCKED, never as a successful UI test.
"""
import argparse
import json
from playwright.sync_api import sync_playwright, expect
from e2e import Harness, ROOT
from e2e_identity import PASSWORD


def run(binary, chromium=None):
    h=Harness(binary,heartbeat='500ms',offline='2s');checks=[]
    def passed(test):checks.append(test);print('PASS',test,flush=True)
    try:
        h.start_server()
        h.cli('auth','password','set','--password-stdin','--config',h.admin,stdin=PASSWORD+'\n')
        with sync_playwright() as p:
            options={'headless':True}
            if chromium:options['executable_path']=chromium
            browser=p.chromium.launch(**options)
            contexts=[browser.new_context(ignore_https_errors=True,viewport={'width':1440,'height':1000}) for _ in range(3)]
            owner,a,b=[c.new_page() for c in contexts]
            errors=[]
            try:
                for page in [owner,a,b]:
                    page.on('pageerror',lambda e:errors.append(str(e)))
                    page.on('dialog',lambda d:d.accept())
                    page.goto(h.url,wait_until='domcontentloaded')
                    expect(page.locator('#web-password')).to_be_visible()
                ticket=h.cli('login','create','--config',h.admin)['ticket']
                owner.locator('#mode-ticket').click();owner.locator('#ticket').fill(ticket);owner.locator('#login-submit').click()
                expect(owner.locator('#app')).to_be_visible()
                for page in [a,b]:
                    page.locator('#client-name').fill('Windows participant')
                    page.locator('#web-password').fill(PASSWORD);page.locator('#login-submit').click()
                    expect(page.locator('#app')).to_be_visible()
                    # Controls are removed by capabilities, including the navigation.
                    expect(page.locator('[data-view="devices"]')).to_be_hidden()
                    page.locator('[data-view="settings"]').click()
                    expect(page.locator('#password-settings')).to_be_hidden()
                    expect(page.locator('#bind-cli')).to_be_visible()
                    expect(page.locator('#storage-panel')).to_be_hidden()
                oa=contexts[1].request.get(h.url+'/api/v1/me').json()
                ob=contexts[2].request.get(h.url+'/api/v1/me').json()
                assert oa['id']!=ob['id'] and oa['role']==ob['role']=='member'
                assert oa['source']['clientId']!=ob['source']['clientId']
                owner.locator('[data-view="settings"]').click()
                expect(owner.locator('#password-settings')).to_be_visible()
                expect(owner.locator('[data-view="devices"]')).to_be_visible()
                passed('one Owner credential and two independent same-password Members; role-specific menus and buttons')
                for path,method,data in [('/api/v1/storage','GET',None),('/api/v1/auth/password','PUT',{'password':'unauthorized change secret'})]:
                    r=contexts[2].request.fetch(h.url+path,method=method,data=data,headers={'X-SLB-CSRF':ob['csrf']})
                    assert r.status==403,(path,r.status)
                passed('hidden controls backed by HTTP 403, not UI-only authorization')
                for page in [a,b]:page.locator('[data-view="note"]').click();expect(page.locator('#note-text')).to_be_enabled()
                text='独立身份协同\n  自动保存与 SSE 同步'
                a.locator('#note-text').fill(text);expect(a.locator('#note-status')).to_contain_text('已保存');expect(b.locator('#note-text')).to_have_value(text)
                note=contexts[2].request.get(h.url+'/api/v1/note').json()
                assert note['updatedBy']==oa['id'] and note['source']['clientId']==oa['source']['clientId']
                passed('autosave and SSE work across distinct Member identities with source provenance')
                b.locator('#note-text').dispatch_event('compositionstart');b.locator('#note-text').fill('B 未提交草稿')
                a.locator('#note-text').fill('A 已提交的版本');expect(b.locator('#note-conflict')).to_be_visible()
                expect(b.locator('#note-text')).to_have_value('B 未提交草稿')
                b.locator('#note-text').dispatch_event('compositionend');b.locator('#note-keep-local').click()
                expect(a.locator('#note-text')).to_have_value('B 未提交草稿')
                passed('cross-identity CAS conflict retains local draft and requires explicit resolution')
                b.reload(wait_until='domcontentloaded');expect(b.locator('#app')).to_be_visible()
                assert contexts[2].request.get(h.url+'/api/v1/me').json()['id']==ob['id']
                b.locator('[data-view="settings"]').click();b.locator('#bind-cli').click()
                expect(b.locator('#client-bind-dialog')).to_be_visible()
                code=b.locator('#client-bind-code').input_value();profile=h.root/'bound-cli.json'
                out=h.cli('client','bind',h.url,'--fingerprint',h.owner['fingerprint'],'--name','B CLI','--code-stdin','--config',profile,stdin=code+'\n')
                assert out['principal']['id']==ob['id'] and out['client']['id']!=ob['source']['clientId']
                b.locator('#client-bind-dialog [data-close]').click()
                expect(b.locator('#client-bind-code')).to_have_value('')
                passed('refresh retains identity; explicit browser-to-CLI binding creates a separate credential')
                b.locator('[data-view="participants"]').click()
                # Member can create a Bot, but cannot choose human/admin types.
                b.locator('#add-principal').click()
                expect(b.locator('#principal-type')).to_be_disabled();expect(b.locator('#principal-type')).to_have_value('bot')
                b.locator('#principal-dialog [data-close]').click()
                passed('Member Bot creation UI scoped to its own Bots, never administrator/human creation')
                h.cli('client','revoke',out['client']['id'],'--config',h.admin)
                assert h.cli('identity','whoami','--config',profile,expect_ok=False)['error']['code']=='principal_inactive'
                assert contexts[2].request.get(h.url+'/api/v1/me').status==200
                assert contexts[1].request.get(h.url+'/api/v1/me').status==200
                passed('CLI credential revocation leaves its linked browser and other Member intact')
                for page in [owner,a,b]:page.locator('[data-view="note"]').click()
                b.set_viewport_size({'width':390,'height':844});assert b.evaluate('() => document.documentElement.scrollWidth <= innerWidth')
                a.screenshot(path=str(ROOT/'reports/identity-member-live.png'),full_page=True)
                owner.screenshot(path=str(ROOT/'reports/identity-owner-live.png'),full_page=True)
                b.screenshot(path=str(ROOT/'reports/identity-mobile-live.png'),full_page=True)
                passed('desktop/mobile layout renders real shared state without horizontal overflow')
                assert not errors,errors
                return {'status':'PASS','count':len(checks),'checks':checks,'networked':True,'syntheticData':False,'browser':'Chromium','physicalMacWindows':False}
            finally:browser.close()
    finally:h.close()

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--binary',default=ROOT/'bin/seven-air-link');p.add_argument('--chromium');a=p.parse_args()
    out=ROOT/'reports/identity-ui-live.json';out.parent.mkdir(exist_ok=True)
    try:r=run(a.binary,a.chromium)
    except Exception as e:
        r={'status':'BLOCKED' if 'ERR_BLOCKED_BY_ADMINISTRATOR' in str(e) else 'FAIL','error':str(e),'networkedAcceptancePassed':False}
        out.write_text(json.dumps(r,indent=2,ensure_ascii=False)+'\n');raise
    out.write_text(json.dumps(r,indent=2,ensure_ascii=False)+'\n');print(json.dumps(r,ensure_ascii=False))
