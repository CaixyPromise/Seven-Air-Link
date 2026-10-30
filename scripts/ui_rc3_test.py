#!/usr/bin/env python3
"""Real Chromium + TLS Server E2E. No in-page network substitutes or policy bypass."""
import argparse,json
from playwright.sync_api import sync_playwright
from e2e import Harness,ROOT
PASSWORD='Browser test password 2026!'
NEW_PASSWORD='Changed browser password 2026!'
def run(binary,chromium=None):
    h=Harness(binary,heartbeat='500ms',offline='2s');checks=[]
    def passed(name):checks.append(name);print('PASS',name,flush=True)
    try:
        h.start_server();h.cli('auth','password','set','--password-stdin','--config',h.admin,stdin=PASSWORD+'\n')
        with sync_playwright() as p:
            launch={'headless':True}
            if chromium:launch['executable_path']=chromium
            browser=p.chromium.launch(**launch)
            ca=browser.new_context(ignore_https_errors=True,viewport={'width':1440,'height':1000});cb=browser.new_context(ignore_https_errors=True,viewport={'width':1300,'height':950})
            a,b=ca.new_page(),cb.new_page();errors=[]
            for page in (a,b):
                page.on('pageerror',lambda e:errors.append(str(e)));page.on('dialog',lambda d:d.accept());page.goto(h.url,wait_until='domcontentloaded')
                page.locator('#web-password').wait_for(state='visible');page.locator('#web-password').fill(PASSWORD);page.locator('#login-submit').click();page.locator('#app').wait_for(state='visible')
                page.locator('[data-view="note"]').click();page.wait_for_function("!document.querySelector('#note-text').disabled")
            passed('Two isolated browser contexts sign in with a fixed password, no ticket')
            assert a.locator('#web-password').input_value()==''
            content='开发说明\nWindows API: localhost:8080\n  原始缩进  '
            a.locator('#note-text').fill(content);a.wait_for_function("document.querySelector('#note-status').textContent.includes('已保存')")
            b.wait_for_function("document.querySelector('#note-text').value === "+json.dumps(content));passed('Textarea autosave reaches a second real browser through SSE')
            a.reload(wait_until='domcontentloaded');a.locator('#app').wait_for(state='visible');a.locator('[data-view="note"]').click();a.wait_for_function("document.querySelector('#note-text').value === "+json.dumps(content));passed('Reload retains authenticated Cookie and persisted note')
            b.locator('#note-text').dispatch_event('compositionstart');b.locator('#note-text').fill('B 尚未提交的本机草稿');a.locator('#note-text').fill('A 已提交的新版本')
            b.locator('#note-conflict').wait_for(state='visible');assert b.locator('#note-text').input_value()=='B 尚未提交的本机草稿';assert b.locator('#note-remote').input_value()=='A 已提交的新版本'
            b.locator('#note-text').dispatch_event('compositionend');b.locator('#note-keep-local').click();a.wait_for_function("document.querySelector('#note-text').value==='B 尚未提交的本机草稿'");passed('Concurrent editing retains draft; explicit resolution uses fresh CAS')
            profile=h.root/'bot.json';h.cli('bot','bind','--name','Notebook Bot','--out',profile,'--config',h.admin);note=h.cli('note','get','--config',profile);hostile='<img src=x onerror=window.__noteXSS=1>'
            h.cli('note','set','--revision',note['revision'],'--text',hostile,'--config',profile);a.wait_for_function("document.querySelector('#note-text').value === "+json.dumps(hostile));assert a.locator('#view-note img').count()==0 and a.evaluate('window.__noteXSS===undefined');passed('Bot CLI shares the same note; text never becomes HTML')
            a.screenshot(path=str(ROOT/'reports/ui-rc3-notes-live.png'),full_page=True);b.set_viewport_size({'width':390,'height':844});assert b.evaluate('document.documentElement.scrollWidth<=innerWidth');b.screenshot(path=str(ROOT/'reports/ui-rc3-notes-mobile-live.png'),full_page=True)
            a.locator('[data-view="settings"]').click();a.wait_for_function("document.querySelectorAll('#session-list .session-card').length===2")
            a.locator('#current-web-password').fill(PASSWORD);a.locator('#new-web-password').fill(NEW_PASSWORD);a.locator('#confirm-web-password').fill(NEW_PASSWORD);a.locator('#password-change-form button[type="submit"]').click()
            for page in (a,b):page.locator('#login').wait_for(state='visible',timeout=10000)
            passed('Password change logs out both Owner sessions and clears UI state');assert a.locator('#note-text').input_value()=='' and b.locator('#note-text').input_value()==''
            assert h.cli('identity','whoami','--config',profile)['role']=='member'
            a.locator('#web-password').fill(NEW_PASSWORD);a.locator('#login-submit').click();a.locator('#app').wait_for(state='visible');a.locator('[data-view="settings"]').click();a.locator('#revoke-all-sessions').click();a.locator('#login').wait_for(state='visible');passed('Own-session revocation does not revoke independent Bot credentials')
            ticket=h.cli('login','create','--config',profile)['ticket'];b.locator('#mode-ticket').click();b.locator('#ticket').fill(ticket);b.locator('#login-submit').click();b.locator('#app').wait_for(state='visible');b.locator('[data-view="note"]').click();b.wait_for_function("!document.querySelector('#note-text').disabled")
            b.locator('#note-text').fill('Bot browser verified');b.wait_for_function("document.querySelector('#note-status').textContent.includes('已保存')");assert h.cli('note','get','--config',profile)['text']=='Bot browser verified';assert not b.locator('#password-settings').is_visible();passed('Existing ticket login and Member note editing remain compatible')
            assert not errors,errors;browser.close()
        return {'status':'PASS','networked':True,'syntheticData':False,'checks':checks,'count':len(checks),'platform':'Chromium with real HTTPS/SSE Server','testTrust':'ignore_https_errors only in disposable test browser contexts; application TLS pinning unchanged'}
    finally:h.close()
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--binary',default=ROOT/'bin/seven-air-link');p.add_argument('--chromium');a=p.parse_args();(ROOT/'reports').mkdir(exist_ok=True)
    try:r=run(a.binary,a.chromium)
    except Exception as e:
        r={'status':'BLOCKED' if 'ERR_BLOCKED_BY_ADMINISTRATOR' in str(e) else 'FAIL','reason':str(e),'networked':False,'policyBypass':False};(ROOT/'reports/ui-rc3-live.json').write_text(json.dumps(r,ensure_ascii=False,indent=2)+'\n');raise
    (ROOT/'reports/ui-rc3-live.json').write_text(json.dumps(r,ensure_ascii=False,indent=2)+'\n');print(json.dumps(r,ensure_ascii=False))
