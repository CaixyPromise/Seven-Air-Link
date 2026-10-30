#!/usr/bin/env python3
"""Real HTTPS/SSE processes: passwords, note CAS, restart and backup/restore."""
import argparse, concurrent.futures, http.cookiejar, json, ssl, urllib.request, urllib.error
from e2e import Harness, ROOT, wait_for
PASSWORD='Correct battery horse 便签 2026'
NEW_PASSWORD='New correct battery horse 2026'
def browser(h):
    return urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()),urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(h.data/'server.crt'))))
def call(h,opener,method,path,body=None,csrf=None):
    headers={'Origin':h.url}
    if body is not None: headers['Content-Type']='application/json'
    if csrf: headers['X-SLB-CSRF']=csrf
    req=urllib.request.Request(h.url+path,data=None if body is None else json.dumps(body).encode(),method=method,headers=headers)
    try:r=opener.open(req,timeout=8)
    except urllib.error.HTTPError as e:r=e
    with r:return r.status,json.loads(r.read())
def run(binary):
    h=Harness(binary,heartbeat='500ms',offline='2s');checks=[]
    def passed(name):checks.append(name);print('PASS',name,flush=True)
    try:
        h.start_server();assert not h.cli('auth','password','status','--config',h.admin)['enabled']
        h.cli('auth','password','set','--password-stdin','--config',h.admin,stdin=PASSWORD+'\n')
        assert h.cli('auth','password','status','--config',h.admin)['enabled']
        a,b=browser(h),browser(h)
        for op in (a,b):
            status,r=call(h,op,'POST','/api/v1/sessions/password',{'password':PASSWORD});assert status==200,(status,r)
        csrfA=call(h,a,'GET','/api/v1/me')[1]['csrf'];csrfB=call(h,b,'GET','/api/v1/me')[1]['csrf']
        passed('Two independent HTTPS Cookie clients login without one-use tickets')
        profile=h.root/'bot.json';result=h.cli('bot','bind','--name','CLI Note Bot','--out',profile,'--config',h.admin);botid=result['principal']['id']
        denied=h.cli('auth','password','set','--password-stdin','--config',profile,stdin=NEW_PASSWORD+'\n',expect_ok=False)
        assert denied['error'];passed('Bot cannot administer Owner password')
        initial=h.cli('note','get','--config',profile);assert initial['revision']==0
        content='开发说明\n\nSELECT * FROM orders;\n  保留缩进和末尾空格  \n'
        saved=h.cli('note','set','--revision','0','--stdin','--config',profile,stdin=content);assert saved['text']==content and saved['revision']==1
        assert call(h,b,'GET','/api/v1/note')[1]['text']==content
        conflict=h.cli('note','set','--revision','0','--text','stale draft','--config',profile,expect_ok=False)
        assert conflict['error']['code']=='note_revision_conflict' and conflict['note']['text']==content
        passed('CLI UTF-8/whitespace fidelity and structured revision conflict')
        def put(op,csrf,text):return call(h,op,'PUT','/api/v1/note',{'text':text,'baseRevision':1},csrf)
        with concurrent.futures.ThreadPoolExecutor(2) as pool:
            futures=[pool.submit(put,a,csrfA,'environment-A'),pool.submit(put,b,csrfB,'environment-B')];results=[f.result() for f in futures]
        assert sorted(r[0] for r in results)==[200,409],results
        passed('Concurrent HTTPS writers produce exactly one CAS commit')
        watch=h.launch(['note','watch','--timeout','12s','--max-updates','2','--config',profile,'--output','ndjson'],'note-watch')
        wait_for(lambda:any('"revision"' in f.read_text() for f in h.root.glob('*note-watch.stdout')),detail='initial note SSE snapshot')
        cur=h.cli('note','get','--config',profile);h.cli('note','set','--revision',cur['revision'],'--text','real SSE update','--config',profile)
        assert watch.wait(timeout=10)==0
        updates=[json.loads(line) for line in next(h.root.glob('*note-watch.stdout')).read_text().splitlines() if line.strip()]
        assert updates[-1]['text']=='real SSE update' and len(updates)==2
        passed('Real CLI SSE receives initial snapshot and new update')
        assert call(h,a,'PUT','/api/v1/note',{'text':'withoutCSRF','baseRevision':3})[0]==403
        passed('Cookie note changes require CSRF')
        ticket=h.cli('login','create','--config',h.admin)['ticket']
        h.cli('auth','password','set','--password-stdin','--config',h.admin,stdin=NEW_PASSWORD+'\n')
        assert call(h,a,'GET','/api/v1/me')[0]==401 and call(h,b,'GET','/api/v1/me')[0]==401
        assert call(h,browser(h),'POST','/api/v1/sessions',{'ticket':ticket})[0]==403
        assert h.cli('identity','whoami','--config',profile)['id']==botid
        passed('Password rotation invalidates Owner cookies/tickets, not Bot tokens')
        saved=h.cli('note','get','--config',profile);before_rev=saved['revision'];h.server.kill();h.server.wait(timeout=4);h.start_server()
        assert call(h,browser(h),'POST','/api/v1/sessions/password',{'password':NEW_PASSWORD})[0]==200
        assert h.cli('note','get','--config',profile)==saved
        passed('SIGKILL restart preserves password and exact note revision/content')
        serialized=json.dumps(h.cli('activity','list','--limit','500','--config',h.admin),ensure_ascii=False)
        assert PASSWORD not in serialized and NEW_PASSWORD not in serialized and content not in serialized and 'pbkdf2' not in serialized
        passed('Activity contains metadata, not password/hash or note contents')
        h.server.terminate();h.server.wait(timeout=5);backup=h.root/'backup';restored=h.root/'restored'
        h.cli('storage','backup','--data',h.data,'--out',backup);assert h.cli('storage','verify','--from',backup)['ok'];assert h.cli('storage','restore','--from',backup,'--data',restored)['ok']
        h.data=restored;h.admin=restored/'admin.json';h.start_server()
        assert call(h,browser(h),'POST','/api/v1/sessions/password',{'password':NEW_PASSWORD})[0]==200
        assert h.cli('note','get','--config',profile)==saved
        passed('Actual restored Server accepts password and serves the same note')
        h.cli('auth','password','disable','--yes','--config',h.admin)
        assert call(h,browser(h),'POST','/api/v1/sessions/password',{'password':NEW_PASSWORD})[0]==403
        ticket=h.cli('login','create','--config',h.admin)['ticket'];assert call(h,browser(h),'POST','/api/v1/sessions',{'ticket':ticket})[0]==200
        passed('Password disable retains ticket recovery, never anonymous access')
        return {'status':'PASS','checks':checks,'count':len(checks),'realProcesses':True,'transport':'HTTPS + SSE + SQLite WAL','modelExecution':False,'noteRevisionAfterRestart':before_rev}
    finally:h.close()
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--binary',default=ROOT/'bin/seven-air-link');a=p.parse_args();(ROOT/'reports').mkdir(exist_ok=True);r=run(a.binary);(ROOT/'reports/rc3-e2e.json').write_text(json.dumps(r,ensure_ascii=False,indent=2)+'\n');print(json.dumps(r,ensure_ascii=False))
