#!/usr/bin/env python3
"""Optional REAL network browser test; blocked policy is reported, never bypassed."""
import argparse,json
from pathlib import Path
from playwright.sync_api import sync_playwright
from e2e import Harness,ROOT,wait_for

def run(binary,chromium):
    h=Harness(binary,heartbeat='500ms',offline='2s')
    try:
        h.start_server();bots=[]
        for name in ('A','B'):
            cfg=h.root/(name+'.json');result=h.cli('bot','bind','--name',name,'--out',cfg,'--config',h.admin);bots.append((cfg,result['principal']['id']))
        with sync_playwright() as p:
            browser=p.chromium.launch(executable_path=chromium,headless=True,args=['--no-sandbox','--disable-dev-shm-usage']);errors=[]
            contexts=[];pages=[]
            for cfg in (h.admin,bots[0][0],bots[1][0]):
                context=browser.new_context(ignore_https_errors=True,viewport={'width':1440,'height':1000});contexts.append(context)
                page=context.new_page();page.on('pageerror',lambda e:errors.append(str(e)))
                page.goto(h.url);page.locator('#ticket').fill(h.cli('login','create','--config',cfg)['ticket']);page.locator('#login-form button').click();page.locator('#app').wait_for(state='visible')
                page.wait_for_function("document.querySelector('#conversation-list').children.length >= 3");pages.append(page)
            owner,a,b=pages
            a.evaluate('(id)=>selectConversation(id)',bots[1][1]);b.evaluate('(id)=>selectConversation(id)',bots[0][1])
            text='<img src=x onerror=window.__xss=1> 中文实时测试'
            a.locator('#message-input').fill(text);a.locator('#send-message').click();b.get_by_text(text,exact=True).wait_for()
            assert not b.evaluate('window.__xss===1');assert b.locator('#message-list img').count()==0
            owner.locator('[data-view="collaboration"]').click();assert owner.get_by_text(text,exact=True).count()==0
            assert not a.locator('[data-view="devices"]').is_visible()
            a.locator('#file-picker').set_input_files({'name':'中文.txt','mimeType':'text/plain','buffer':'附件中文'.encode()})
            a.wait_for_function("document.querySelector('#pending-files').children.length === 1");a.locator('#send-message').click()
            b.get_by_role('link',name='中文.txt',exact=True).wait_for();b.get_by_role('button',name='预览',exact=True).click();b.get_by_text('附件中文',exact=True).wait_for()
            b.locator('[data-close="preview-dialog"]').click()
            a.screenshot(path=str(ROOT/'reports/ui-collaboration-live.png'),full_page=True)
            b.set_viewport_size({'width':390,'height':844});assert b.evaluate('document.documentElement.scrollWidth <= innerWidth')
            b.screenshot(path=str(ROOT/'reports/ui-collaboration-live-mobile.png'),full_page=True)
            h.cli('bot','revoke',bots[1][1],'--config',h.admin);b.locator('#login').wait_for(state='visible',timeout=5000)
            assert not errors,errors
            browser.close()
        return {'status':'PASS','checks':['independent Owner/Bot browser sessions','private live SSE','no remote HTML execution','role-specific controls','file upload and UTF-8 preview','mobile layout','revocation closes browser session'],'networked':True}
    finally:h.close()
if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--binary',default=ROOT/'bin/seven-air-link');parser.add_argument('--chromium',default='/usr/bin/chromium');args=parser.parse_args()
    try:report=run(args.binary,args.chromium)
    except Exception as exc:
        report={'status':'BLOCKED' if 'ERR_BLOCKED_BY_ADMINISTRATOR' in str(exc) else 'FAIL','reason':str(exc),'networked':False,'policyBypass':False}
        (ROOT/'reports/ui-m3-live.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');raise
    (ROOT/'reports/ui-m3-live.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');print(json.dumps(report,ensure_ascii=False))
