#!/usr/bin/env python3
"""Offline capability/DOM regression using explicitly synthetic data.
ZERO network requests. This does not replace real HTTPS/SSE/browser acceptance.
"""
import argparse
import json
import re
from playwright.sync_api import sync_playwright, expect
from e2e import ROOT


def run(chromium):
    html=(ROOT/'web/index.html').read_text()
    html=re.sub(r'<link[^>]+href="/style.css"[^>]*>','',html)
    html=re.sub(r'<script[^>]+src="/[^"]+"[^>]*></script>','',html)
    checks=[]
    with sync_playwright() as p:
        browser=p.chromium.launch(executable_path=chromium,headless=True)
        page=browser.new_page(viewport={'width':1440,'height':1000})
        errors=[];requests=[]
        page.on('pageerror',lambda e:errors.append(str(e)));page.on('request',lambda r:requests.append(r.url))
        try:
            page.set_content(html);page.add_style_tag(content=(ROOT/'web/style.css').read_text())
            page.add_script_tag(content=r'''
window.F={role:'member',id:'human_windows',kind:'human',rev:1,text:'离线界面验收 · 合成数据\n\n共享便签：每个修改保留独立身份。',calls:[]};
const own={id:'human_owner',name:'Server Owner · 示例',type:'human',role:'owner',status:'active'};
const human={id:'human_windows',name:'Windows 客户端 · 示例',type:'human',role:'member',status:'active',ownerId:'human_owner'};
const bot={id:'bot_windows',name:'我的 Bot · 示例',type:'bot',role:'member',status:'active',ownerId:'human_windows'};
const foreign={id:'bot_other',name:'其他客户端 Bot',type:'bot',role:'member',status:'active',ownerId:'human_other'};
function caps(){const o=F.role==='owner',h=F.role==='member'&&F.kind==='human';return{manageServer:o,manageIdentities:o,manageDevices:o,manageStorage:o,createBot:o||h,bindCLI:h,enrollNode:h,manageOwnClients:h,collaborate:true,useServices:true};}
function me(){return{id:F.id,role:F.role,csrf:'synthetic-not-a-real-csrf',principal:F.role==='owner'?own:(F.kind==='bot'?bot:human),capabilities:caps(),source:{clientId:'client_example',kind:'browser',ip:'192.0.2.12'},serverOwnerId:own.id};}
function response(value,status=200){return Promise.resolve({ok:status>=200&&status<300,status,json:async()=>structuredClone(value),text:async()=>JSON.stringify(value)});}
if(!crypto.randomUUID)crypto.randomUUID=()=>String(Math.random());
window.fetch=async function(path,opt={}){
 const method=opt.method||'GET',base=path.split('?')[0],data=opt.body?JSON.parse(opt.body):{};F.calls.push({path:base,method,data});
 if(base==='/api/v1/me')return response(me());
 if(base==='/api/v1/auth/methods')return response({password:true,passwordIdentity:'member',ticket:true,localManagement:F.role==='owner'});
 if(base==='/api/v1/info')return response({version:'0.1.0-rc.4',protocol:1,fingerprint:'0'.repeat(64)});
 if(base==='/api/v1/devices')return response({devices:[]});
 if(base==='/api/v1/services')return response({services:[]});
 if(base==='/api/v1/forwards')return response({forwards:[]});
 if(base==='/api/v1/activity')return response({events:[]});
 if(base==='/api/v1/peers')return response({peers:[own,human,bot,foreign]});
 if(base==='/api/v1/messages')return response({messages:[],hasMore:false,nextCursor:0});
 if(base==='/api/v1/files')return response({files:[]});
 if(base==='/api/v1/sessions')return response({sessions:[]});
 if(base==='/api/v1/auth/password')return response({enabled:true,revision:1});
 if(base==='/api/v1/storage')return response({schema:6,sqliteVersion:'fixture-only',messages:0,files:0,databaseBytes:32768,walBytes:0,fileBytes:0});
 if(base==='/api/v1/clients')return response({clients:[{id:'client_example',principalId:F.id,name:'此浏览器 · 示例',kind:'browser',createdAt:'2026-11-02T00:00:00Z',expiresAt:'2026-12-02T00:00:00Z'}],currentClientId:'client_example'});
 if(base==='/api/v1/client-links')return response({code:'SYNTHETIC-CLI-BIND-CODE',principalId:F.id,expiresAt:'2026-11-02T00:02:00Z'},201);
 if(base==='/api/v1/principals'&&method==='POST')return new Promise(resolve=>{F.resolveInvite=()=>resolve({ok:true,status:201,json:async()=>({principal:bot,code:'LATE-SYNTHETIC-INVITE'})});});
 if(base==='/api/v1/pairings'&&method==='POST')return new Promise(resolve=>{F.resolvePair=()=>resolve({ok:true,status:201,json:async()=>({code:'LATE-SYNTHETIC-PAIR',expiresAt:'2026-11-02T00:05:00Z'})});});
 if(base==='/api/v1/note'){
  if(method==='PUT'){if(data.baseRevision!==F.rev)return response({error:{code:'note_revision_conflict',message:'conflict'},note:{id:'shared',text:F.text,revision:F.rev}},409);F.text=data.text;F.rev++;}
  return response({id:'shared',text:F.text,revision:F.rev,updatedBy:F.id,source:me().source,updatedAt:'2026-11-02T00:00:00Z'});
 }
 throw new Error('missing synthetic fixture '+method+' '+base);
};
window.EventSource=class{constructor(url){this.url=url;this.handlers={};}addEventListener(k,fn){this.handlers[k]=fn;}close(){}};
''')
            for filename in ['app.js','collaboration.js','maintenance.js','notes.js','auth.js','clients.js']:
                page.add_script_tag(content=(ROOT/'web'/filename).read_text())
            expect(page.locator('#app')).to_be_visible()
            expect(page.locator('[data-view="devices"]')).to_be_hidden()
            page.locator('[data-view="settings"]').click()
            expect(page.locator('#password-settings')).to_be_hidden();expect(page.locator('#storage-panel')).to_be_hidden()
            expect(page.locator('#bind-cli')).to_be_visible();expect(page.locator('#enroll-node')).to_be_visible()
            checks.append('Member capability menus; no Server settings/storage/device administration')
            page.locator('#bind-cli').click();expect(page.locator('#client-bind-dialog')).to_be_visible()
            expect(page.locator('#client-bind-code')).to_have_value('SYNTHETIC-CLI-BIND-CODE')
            page.locator('[data-close="client-bind-dialog"]').click();expect(page.locator('#client-bind-code')).to_have_value('')
            checks.append('explicit binding dialog and secret-field cleanup')
            page.locator('[data-view="participants"]').click();page.locator('#add-principal').click()
            expect(page.locator('#principal-type')).to_be_disabled();expect(page.locator('#principal-type')).to_have_value('bot')
            page.locator('[data-close="principal-dialog"]').click()
            checks.append('Member can create owned Bot but cannot select human type')
            page.locator('[data-view="note"]').click();expect(page.locator('#note-text')).to_be_enabled()
            page.locator('#note-text').fill('离线 UI 夹具\n中文输入与自动保存')
            expect(page.locator('#note-status')).to_contain_text('已保存')
            assert page.evaluate('F.text')=='离线 UI 夹具\n中文输入与自动保存'
            checks.append('real frontend autosave handler with synthetic backend (not network proof)')
            page.evaluate('() => {document.querySelector("#connection").textContent="离线 UI · 合成数据";}')
            page.screenshot(path=str(ROOT/'reports/identity-member-offline.png'),full_page=True)
            page.set_viewport_size({'width':390,'height':844})
            assert page.evaluate('() => document.documentElement.scrollWidth<=innerWidth')
            page.screenshot(path=str(ROOT/'reports/identity-mobile-offline.png'),full_page=True)
            checks.append('390px no horizontal overflow')
            page.set_viewport_size({'width':1440,'height':1000})
            page.locator('[data-view="participants"]').click()
            page.locator('#add-principal').click()
            page.locator('#principal-name').fill('Deferred test Bot')
            page.locator('#principal-form button[type="submit"]').click()
            page.wait_for_function('() => typeof F.resolveInvite === "function"')
            page.evaluate('async () => {showLogin();await enter();F.resolveInvite();}')
            expect(page.locator('#principal-form button[type="submit"]')).to_be_enabled()
            expect(page.locator('#principal-dialog')).not_to_be_visible()
            expect(page.locator('#principal-code')).to_have_value('')
            checks.append('Delayed Bot enrollment response cannot reopen a secret dialog after identity reset')
            page.evaluate('() => {showLogin();F.role="owner";F.id="human_owner";enter();}')
            expect(page.locator('#app')).to_be_visible();expect(page.locator('[data-view="devices"]')).to_be_visible()
            page.locator('[data-view="settings"]').click();expect(page.locator('#password-settings')).to_be_visible();expect(page.locator('#storage-panel')).to_be_visible();expect(page.locator('#bind-cli')).to_be_hidden()
            page.evaluate('() => {document.querySelector("#connection").textContent="离线 UI · 合成数据";}')
            page.screenshot(path=str(ROOT/'reports/identity-owner-offline.png'),full_page=True)
            checks.append('Owner-only management capabilities shown without granting them to Members')
            page.locator('[data-view="devices"]').click()
            page.locator('#new-pair').click()
            page.locator('#generate-pair').click()
            page.wait_for_function('() => typeof F.resolvePair === "function"')
            page.evaluate('async () => {showLogin();F.role="member";F.id="human_windows";await enter();F.resolvePair();}')
            expect(page.locator('#generate-pair')).to_be_enabled()
            expect(page.locator('#pair-dialog')).not_to_be_visible()
            expect(page.locator('#pair-code')).to_have_value('')
            checks.append('Delayed Owner pairing response cannot populate a Member session')
            page.evaluate('() => {showLogin();F.role="member";F.id="bot_windows";F.kind="bot";enter();}')
            expect(page.locator('#app')).to_be_visible();page.locator('[data-view="participants"]').click();expect(page.locator('#add-principal')).to_be_hidden()
            page.locator('[data-view="settings"]').click();expect(page.locator('#bind-cli')).to_be_hidden();expect(page.locator('#password-settings')).to_be_hidden()
            checks.append('Bot has no nested identity management or joining-password controls')
            page.evaluate('() => showLogin()');expect(page.locator('#client-bind-code')).to_have_value('');expect(page.locator('#note-text')).to_have_value('')
            checks.append('logout clears identity-scoped draft, clients and binding fields')
            assert not errors,errors
            assert not requests,requests
            return {'status':'PASS','count':len(checks),'checks':checks,'networkRequests':0,'syntheticData':True,'notNetworkE2E':True}
        finally:browser.close()

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--chromium',default='/usr/bin/chromium');a=p.parse_args();(ROOT/'reports').mkdir(exist_ok=True)
    r=run(a.chromium);(ROOT/'reports/identity-ui-offline.json').write_text(json.dumps(r,ensure_ascii=False,indent=2)+'\n');print(json.dumps(r,ensure_ascii=False))
