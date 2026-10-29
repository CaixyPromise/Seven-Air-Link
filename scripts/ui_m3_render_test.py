#!/usr/bin/env python3
"""Offline synthetic DOM/visual/interaction tests, NOT real browser network E2E.
about:blank + inline actual assets + in-memory HTTP/SSE/XHR doubles; zero requests.
"""
import argparse,json,re
from pathlib import Path
from playwright.sync_api import sync_playwright
ROOT=Path(__file__).resolve().parents[1]
def run(chromium):
    now='2026-10-28T13:40:00Z'
    peers=[{'id':'human_owner','name':'Marverick','type':'human','role':'owner','status':'active','listening':1},
           {'id':'bot_mac','name':'Mac Coding Bot','type':'bot','role':'member','status':'active','listening':1,'deviceId':'dev_mac','expiresAt':'2026-11-27T13:40:00Z'},
           {'id':'bot_win','name':'Windows Bot','type':'bot','role':'member','status':'active','listening':1,'deviceId':'dev_win','expiresAt':'2026-11-27T13:40:00Z'},
           {'id':'human_dev','name':'Developer','type':'human','role':'member','status':'active','listening':0}]
    messages=[{'id':'msg_1','seq':1,'senderId':'human_owner','recipientId':'shared','text':'Windows 上的接口已经共享。两个 Bot 可以通过 CLI 交换测试结果，服务访问仍需要单独授权。','attachments':[],'status':'accepted','createdAt':now},
              {'id':'msg_2','seq':2,'senderId':'bot_mac','recipientId':'shared','text':'已收到开发上下文。\n\nseven-air-link services list --config mac-bot.json --output json\n\n这里是界面测试数据，不代表 AI 正在执行任务。','attachments':[],'status':'accepted','createdAt':now,'correlationId':'dev-review-01'},
              {'id':'msg_3','seq':3,'senderId':'bot_win','recipientId':'shared','text':'报告已准备好。附件只包含本次验证结果，凭证和网络数据不进入活动记录。','attachments':['file_fixture_0123456789012345'],'status':'accepted','createdAt':now},
              {'id':'msg_4','seq':4,'senderId':'bot_mac','recipientId':'human_owner','text':'这是一条私信的离线 UI 夹具。','attachments':[],'status':'accepted','createdAt':now}]
    fixture={'me':{'id':'human_owner','role':'owner','csrf':'offline-not-a-credential','principal':peers[0]},'peers':{'peers':peers},'info':{'version':'0.1.0-rc.2','protocol':1,'fingerprint':'0'*64,'proxyPort':7778},
       'devices':{'devices':[{'id':id,'name':name,'os':os,'arch':'amd64','nodeVersion':'0.1.0-rc.2','status':'online','ip':ip,'lastSeen':now} for id,name,os,ip in [('dev_mac','Mac-Dev · 示例','linux','192.0.2.10'),('dev_win','Windows-Dev · 示例','linux','192.0.2.11')]]},
       'services':{'services':[{'id':'svc_fixture','name':'win-api','protocol':'http','target':'127.0.0.1:8080','ownerDeviceId':'dev_win','ownerPrincipalId':'human_owner','allowDevices':['dev_mac'],'allowPrincipals':['human_owner','bot_mac'],'status':'online','activeConnections':1,'disabled':False}]},
       'forwards':{'forwards':[]},'activity':{'events':[{'seq':i,'actorId':actor,'action':action,'resourceId':resource,'result':'success','time':now,'correlationId':'dev-review-01'} for i,actor,action,resource in [(1,'human_owner','principal.created','bot_mac'),(2,'bot_mac','message.accepted','msg_2'),(3,'bot_mac','forward.opened','fwd_fixture')]]},'messages':messages,'files':{'file_fixture_0123456789012345':{'id':'file_fixture_0123456789012345','name':'接口测试报告.txt','size':189,'sha256':'a'*64}},'storage':{'sqliteVersion':'3.46.1','schema':4,'messages':128,'files':3,'databaseBytes':32768,'walBytes':4096,'fileBytes':2097152},'calls':[]}
    html=(ROOT/'web/index.html').read_text();html=re.sub(r'<link[^>]+href="/style.css"[^>]*>','',html);html=re.sub(r'<script[^>]+src="/[^"]+"[^>]*></script>','',html)
    with sync_playwright() as p:
        browser=p.chromium.launch(executable_path=chromium,headless=True,args=['--no-sandbox','--disable-dev-shm-usage']);page=browser.new_page(viewport={'width':1440,'height':1080},device_scale_factor=1)
        errors=[];requests=[];page.on('pageerror',lambda e:errors.append(str(e)));page.on('request',lambda r:requests.append(r.url))
        page.set_content(html);page.add_style_tag(content=(ROOT/'web/style.css').read_text())
        page.add_script_tag(content='window.F='+json.dumps(fixture,ensure_ascii=False)+';'+r'''
let fakeID=50;if(!crypto.randomUUID)crypto.randomUUID=()=>`offline-${++fakeID}`;
function fakeResponse(data,status=200){return {ok:status<400,status,json:async()=>structuredClone(data),arrayBuffer:async()=>new TextEncoder().encode("测试报告：中文编码正常。\n通过：18；失败：0。").buffer};}
window.fetch=async(path,opts={})=>{
 const method=opts.method||'GET',u=new URL(path,'https://offline.example'),base=u.pathname;F.calls.push({path,method,body:opts.body?JSON.parse(opts.body):null});
 if(base==='/api/v1/messages' && method==='GET'){
  let rows=F.messages.filter(m=>{const withID=u.searchParams.get('with');return withID==='shared'?m.recipientId==='shared':(m.senderId==='human_owner'&&m.recipientId===withID)||(m.recipientId==='human_owner'&&m.senderId===withID)});
  if(u.searchParams.has('before'))rows=rows.filter(m=>m.seq<Number(u.searchParams.get('before')));
  return fakeResponse({messages:rows.slice().reverse(),hasMore:false,nextCursor:rows[0]?.seq||0,snapshotSeq:Math.max(...F.messages.map(m=>m.seq))});
 }
 if(base==='/api/v1/messages'&&method==='POST'){
  const q=JSON.parse(opts.body);const m={id:'msg_'+(++fakeID),seq:fakeID,senderId:F.me.id,recipientId:q.to,text:q.text,attachments:q.attachments,status:'accepted',createdAt:'2026-10-28T13:40:00Z'};F.messages.push(m);
  if(window.__deferSend)return new Promise(resolve=>{window.__resolveSend=()=>resolve(fakeResponse({message:m,duplicate:false},201))});
  return fakeResponse({message:m,duplicate:false},201);
 }
 if(base.startsWith('/api/v1/files/')){const id=base.split('/')[4];return fakeResponse(F.files[id]||{},F.files[id]?200:404);}
 if(base==='/api/v1/principals'&&method==='POST'){const q=JSON.parse(opts.body);const principal={id:'bot_fixture_new',...q,role:'member',status:'pending',listening:0};F.peers.peers.push(principal);return fakeResponse({principal,code:'bind_OFFLINE_EXAMPLE_ONLY',expiresAt:'2026-10-28T13:45:00Z'},201);}
 if(base==='/api/v1/sessions'&&method==='DELETE')return fakeResponse({ok:true});
 const key=base.split('/').pop();if(F[key])return fakeResponse(F[key]);throw new Error('missing offline fixture '+path);
};
window.EventSource=class {constructor(url){this.url=url;this.handlers={};this.closed=false;window.__sources??=[];window.__sources.push(this);}addEventListener(k,fn){this.handlers[k]=fn;}close(){this.closed=true;}};
window.XMLHttpRequest=class {
 constructor(){this.upload={};this.status=201;this.aborted=false;}open(method,url){this.url=url;}setRequestHeader(){}abort(){this.aborted=true;this.onabort?.();this.onloadend?.();}
 async send(file){await new Promise(resolve=>setTimeout(resolve,15));if(this.aborted)return;const id='file_fake_'+(++fakeID),meta={id,name:file.name,size:file.size,sha256:'b'.repeat(64)};F.files[id]=meta;this.upload.onprogress?.({lengthComputable:true,loaded:file.size,total:file.size});this.responseText=JSON.stringify(meta);this.onload?.();this.onloadend?.();}
};
''')
        page.add_script_tag(content=(ROOT/'web/app.js').read_text());page.add_script_tag(content=(ROOT/'web/collaboration.js').read_text());page.add_script_tag(content=(ROOT/'web/maintenance.js').read_text())
        page.locator('#view-collaboration').wait_for(state='visible');page.wait_for_function("document.querySelectorAll('.message').length===3")
        page.evaluate("document.querySelector('#connection').lastElementChild.textContent='离线 UI 验收 · 示例数据';document.querySelector('#connection').classList.add('disconnected');document.querySelector('#inbox-status').textContent='离线夹具';")
        assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
        page.screenshot(path=str(ROOT/'reports/ui-collaboration-desktop-offline.png'),full_page=True)
        page.locator('[data-view="settings"]').click();page.locator('#storage-refresh').click();page.wait_for_function("document.querySelector('#storage-summary').textContent.includes('128 条消息')")
        page.screenshot(path=str(ROOT/'reports/ui-storage-desktop-offline.png'),full_page=True)
        page.locator('[data-view="collaboration"]').click()
        # Client-side text-only rendering, not a simulated network XSS success.
        attack='<img src=x onerror=window.__xss=1>'
        page.evaluate('(text)=>{collab.messages[0].text=text;renderMessages()}',attack)
        assert page.locator('.message-text').first.inner_text()==attack and page.locator('#message-list img').count()==0
        assert page.evaluate('window.__xss===undefined')
        page.locator('#message-input').fill('共享频道未发送草稿')
        page.evaluate("selectConversation('bot_mac')")
        assert page.locator('#message-list .message').count()==1
        page.locator('#message-input').fill('私信内容');page.locator('#send-message').click();page.wait_for_function("F.calls.some(c=>c.method==='POST'&&c.path==='/api/v1/messages')")
        assert page.evaluate("F.calls.filter(c=>c.method==='POST'&&c.path==='/api/v1/messages').at(-1).body.to")== 'bot_mac'
        page.evaluate("selectConversation('shared')");assert page.locator('#message-input').input_value()=='共享频道未发送草稿'
        # Synthetic clipboard/file/drop exercise the real DOM handlers, no HTTP.
        page.evaluate("""()=>{const dt=new DataTransfer();dt.items.add(new File(['file body'],'粘贴截图.png',{type:'image/png'}));document.dispatchEvent(new ClipboardEvent('paste',{clipboardData:dt,bubbles:true,cancelable:true}));}""")
        page.wait_for_function("document.querySelector('#pending-files').textContent.includes('粘贴截图.png')")
        page.evaluate("""()=>{const dt=new DataTransfer();dt.items.add(new File(['text'],'拖入文件.txt',{type:'text/plain'}));document.dispatchEvent(new DragEvent('drop',{dataTransfer:dt,bubbles:true,cancelable:true}));}""")
        page.wait_for_function("document.querySelector('#pending-files').textContent.includes('拖入文件.txt')")
        assert page.evaluate('collab.uploading===0')
        page.locator('#message-input').fill('附件引用');page.locator('#send-message').click();page.wait_for_function("document.querySelector('#pending-files').children.length===0")
        page.get_by_role('button',name='预览',exact=True).first.click();page.locator('#preview-dialog').wait_for(state='visible');assert '中文编码正常' in page.locator('#preview-text').inner_text();page.locator('[data-close="preview-dialog"]').click()
        page.locator('[data-view="participants"]').click();assert page.locator('.principal-card').count()==4
        page.screenshot(path=str(ROOT/'reports/ui-participants-desktop-offline.png'),full_page=True)
        page.locator('#add-principal').click();page.locator('#principal-name').fill('新 Bot');page.locator('#principal-form button[type="submit"]').click();page.locator('#principal-result').wait_for(state='visible');assert page.locator('#principal-code').input_value()=='bind_OFFLINE_EXAMPLE_ONLY'
        page.locator('[data-close="principal-dialog"]').click();page.wait_for_function("document.querySelector('#principal-code').value===''")
        page.locator('[data-view="services"]').click();page.get_by_role('button',name='权限',exact=True).click();assert page.locator('#grant-principals input').count()==3;page.locator('[data-close="grant-dialog"]').click()
        page.locator('[data-view="collaboration"]').click();page.set_viewport_size({'width':390,'height':844});assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
        page.screenshot(path=str(ROOT/'reports/ui-collaboration-mobile-offline.png'),full_page=True)
        # Logout clears private data and late send callbacks may not repopulate it.
        page.evaluate('window.__deferSend=true');page.locator('#message-input').fill('must not survive logout');page.locator('#send-message').click();page.wait_for_function('typeof window.__resolveSend==="function"')
        page.evaluate('showLogin();window.__resolveSend()');page.wait_for_timeout(40)
        assert page.locator('#message-list').inner_text()=='' and page.locator('#message-input').input_value()=='' and page.evaluate('fileMeta.size===0')
        # Render a member without administration controls.
        page.evaluate("F.me={id:'bot_mac',role:'member',csrf:'offline-member',principal:F.peers.peers[1]};window.__deferSend=false;enter()")
        page.locator('#app').wait_for(state='visible');page.wait_for_function("document.querySelector('#my-kind').textContent.includes('Member')")
        assert not page.locator('[data-view="devices"]').is_visible();assert not page.locator('#storage-panel').is_visible();assert page.locator('#storage-summary').inner_text()=='尚未读取';page.locator('[data-view="participants"]').click();assert not page.locator('#add-principal').is_visible()
        assert not errors,errors
        assert not requests,requests
        browser.close()
        return {'status':'PASS','networkRequests':0,'syntheticData':True,'notNetworkE2E':True,'checks':['desktop/mobile overflow','message HTML escape','conversation switching/draft preservation','message composition target','synthetic clipboard-file/drop handlers','attachment preview','participant creation dialog and code clearing','Principal service ACL selector','logout and late-response isolation','member UI controls','Owner read-only SQLite diagnostics','storage reset and Member visibility isolation'],'screenshots':['ui-collaboration-desktop-offline.png','ui-participants-desktop-offline.png','ui-collaboration-mobile-offline.png','ui-storage-desktop-offline.png']}
def main():
    p=argparse.ArgumentParser();p.add_argument('--chromium',default='/usr/bin/chromium');args=p.parse_args()
    try:report=run(args.chromium)
    except Exception as exc:
        (ROOT/'reports/ui-m3-offline.json').write_text(json.dumps({'status':'FAIL','error':str(exc),'syntheticData':True},ensure_ascii=False,indent=2)+'\n');raise
    (ROOT/'reports/ui-m3-offline.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n');print(json.dumps(report,ensure_ascii=False))
if __name__=='__main__':main()
