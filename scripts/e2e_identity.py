#!/usr/bin/env python3
"""Identity acceptance over actual Go processes/TLS, with no fake auth.
Use --isolated via test_identity_linux.sh for two independent network stacks.
Outputs contain public IDs and conclusions only; no cookies/tokens/passwords.
"""
from __future__ import annotations
import argparse
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import time

from e2e import Harness, ROOT, wait_for
from e2e_m2 import NetworkHarness, Echo, ThreadedTCP, spawn_server
from e2e_two_linux import IsolatedHarness, NS_B
from identity_http_probe import request as http_request

PASSWORD = 'Identity boundary acceptance 2026!'


def suite(binary, isolated=False):
    h = IsolatedHarness(binary) if isolated else NetworkHarness(binary)
    a = h.env_a if isolated else h.root/'environment-a'
    b = h.env_b if isolated else h.root/'environment-b'
    a.mkdir(exist_ok=True, mode=0o700); b.mkdir(exist_ok=True, mode=0o700)
    checks = []
    fixtures = []
    sockets = []
    def passed(test, **detail):
        checks.append({'test':test,'result':'PASS',**detail})
        print('PASS',test,flush=True)
    def endpoint(env):
        return f'https://10.231.0.11:{h.port}' if isolated and env==b else h.url
    def probe(env, jar, path, method='GET', body=None, headers=None):
        q={'url':endpoint(env)+path,'method':method,'ca':str(h.data/'server.crt'),'jar':str(jar) if jar else None,'headers':headers or {}}
        if body is not None:q['body']=body
        if isolated and env==b:
            p=subprocess.run(['nsenter','--net=/run/netns/'+NS_B,sys.executable,str(ROOT/'scripts/identity_http_probe.py')],input=json.dumps(q),text=True,capture_output=True,timeout=12)
            if p.returncode:raise AssertionError(p.stderr)
            return json.loads(p.stdout)
        return http_request(q)
    def login_browser(env, label):
        jar=env/(label+'.cookies')
        out=probe(env,jar,'/api/v1/sessions/password','POST',{'password':PASSWORD,'name':'same display name'})
        assert out['status']==200,out
        me=probe(env,jar,'/api/v1/me')['data']
        assert me['role']=='member' and not me['capabilities']['manageServer']
        return jar,me
    def cfg_login(env,name,bind_code=None):
        profile=env/(name+'.json')
        flags=['client','bind' if bind_code else 'login',endpoint(env),'--fingerprint',h.owner['fingerprint'],'--name',name,'--config',profile]
        flags += ['--code-stdin' if bind_code else '--password-stdin']
        out=h.cli(*flags,stdin=(bind_code or PASSWORD)+'\n')
        return profile,out
    def echo_at(env,port,content=b'client boundary\x00\xff'):
        if isolated and env==b:
            code='import socket; s=socket.create_connection(("127.0.0.1",'+str(port)+'),timeout=5);p='+repr(content)+';s.sendall(p);assert s.recv(len(p))==p;s.close()'
            p=subprocess.run(['nsenter','--net=/run/netns/'+NS_B,sys.executable,'-c',code],capture_output=True,text=True,timeout=8)
            assert p.returncode==0,p.stderr
        else:
            with socket.create_connection(('127.0.0.1',port),timeout=5) as s:
                s.sendall(content);assert s.recv(len(content))==content
    try:
        if isolated:
            nsa=os.readlink('/proc/self/ns/net')
            nsb=subprocess.check_output(['nsenter','--net=/run/netns/'+NS_B,'readlink','/proc/self/ns/net'],text=True).strip()
            assert nsa!=nsb
            h.fixtures()
        h.start_server()
        h.cli('auth','password','set','--password-stdin','--config',h.admin,stdin=PASSWORD+'\n')
        aj,am=login_browser(a,'browser');bj,bm=login_browser(b,'browser')
        assert am['id']!=bm['id'] and am['source']['clientId']!=bm['source']['clientId']
        if isolated:assert bm['source']['ip']=='10.231.0.12',bm
        passed('one Server; same joining password/name yields distinct Member identities',differentNetworkNamespaces=isolated)
        denied=[('/api/v1/storage','GET',None),('/api/v1/pairings','POST',{'ttlSeconds':60}),('/api/v1/auth/password','PUT',{'password':'another valid secret 2026'}),('/api/v1/principals','POST',{'type':'human','name':'forged admin'})]
        for path,method,body in denied:
            r=probe(b,bj,path,method,body,{'X-SLB-CSRF':bm['csrf']})
            assert r['status']==403,(path,r)
        passed('Member direct management API calls fail server-side, regardless of UI')
        # A real remote connection carrying an Owner token must still be rejected.
        if isolated:
            r=probe(b,None,'/api/v1/me',headers={'Authorization':'Bearer '+h.owner['token'],'X-Forwarded-For':'127.0.0.1'})
            assert r['status']==403 and r['data']['error']['code']=='owner_local_only',r
            r=probe(a,None,'/api/v1/me');assert r['status']==401
            assert h.cli('identity','whoami','--config',h.admin)['role']=='owner'
            passed('Owner requires local connection AND Server credential; spoofed forwarded IP grants nothing')
        standalone,sm=cfg_login(b,'standalone')
        assert sm['principal']['id']!=bm['id']
        link=probe(b,bj,'/api/v1/client-links','POST',{}, {'X-SLB-CSRF':bm['csrf']})
        assert link['status']==201,link
        linked,lm=cfg_login(b,'linked',link['data']['code'])
        assert lm['principal']['id']==bm['id'] and lm['client']['id']!=bm['source']['clientId']
        again=probe(b,None,'/api/v1/clients/claim','POST',{'code':link['data']['code'],'name':'replay'})
        assert again['status']==403,again
        passed('same IP does not link browser/CLI; explicit one-use binding gives a separate credential')
        # The remote member enrolls its own Node, not a second Server.
        bc=b/'node.json'
        br=h.cli('client','node','--name','Client B','--out',bc,'--config',linked)
        bn=h.node(bc)
        ac,ad=h.pair('Server local Node','node-a')
        an=h.node(ac)
        wait_for(lambda:h.status()['onlineDevices']==2)
        dev=next(x for x in h.status()['devices'] if x['id']==br['deviceId'])
        assert dev['ownerId']==bm['id'],dev
        passed('client enrolls a Node with its own identity; no Owner profile or second Server')
        # Separate same-principal CLI credentials each maintain a real TCP flow.
        echo_port=8090
        if not isolated:
            fixture=spawn_server(ThreadedTCP(('127.0.0.1',0),Echo));fixtures.append(fixture);echo_port=fixture.server_address[1]
        service=h.cli('share','tcp','--name','server-echo','--target',f'127.0.0.1:{echo_port}','--allow-principals',bm['id'],'--config',ac)
        wait_for(lambda:h.service_state(service['id'])=='online')
        fp,port,lease=h.mapping(linked,'server-echo');echo_at(b,port)
        if isolated:
            try:
                s=socket.create_connection(('127.0.0.1',port),timeout=.3);s.close();raise AssertionError('remote mapping listening in Server namespace')
            except (ConnectionRefusedError,TimeoutError):pass
            h.cli('share','http','--name','server-web','--target','127.0.0.1:8080','--allow-principals',bm['id'],'--config',ac)
            _,wp,_=h.mapping(linked,'server-web')
            code='import urllib.request,json;assert json.load(urllib.request.urlopen("http://127.0.0.1:8080"))["environment"]=="B";assert json.load(urllib.request.urlopen("http://127.0.0.1:'+str(wp)+'"))["environment"]=="A"'
            r=subprocess.run(['nsenter','--net=/run/netns/'+NS_B,sys.executable,'-c',code],capture_output=True,text=True,timeout=8);assert r.returncode==0,r.stderr
        passed('mapping listens on invoking client, resolves explicitly granted Server-side localhost',differentLoopbacks=isolated)
        # Browser note changes carry actual caller, source and CAS revision.
        na=probe(a,aj,'/api/v1/note')['data']
        nr=probe(a,aj,'/api/v1/note','PUT',{'text':'A: 开发备注\n  保留缩进','baseRevision':na['revision']},{'X-SLB-CSRF':am['csrf']});assert nr['status']==200,nr
        conflict=probe(b,bj,'/api/v1/note','PUT',{'text':'B stale draft','baseRevision':na['revision']},{'X-SLB-CSRF':bm['csrf']});assert conflict['status']==409,conflict
        note=h.cli('note','get','--config',linked)
        assert note['updatedBy']==am['id'] and note['source']['clientId']==am['source']['clientId']
        h.cli('note','set','--revision',note['revision'],'--text','B: CLI 已提交','--config',linked)
        note=probe(a,aj,'/api/v1/note')['data']
        assert note['updatedBy']==bm['id'] and note['source']['clientId']==lm['client']['id'] and note['source']['kind']=='cli'
        passed('shared note CAS, author, credential source and revision are distinct across clients')
        bot=b/'bot.json';botout=h.cli('bot','bind','--name','B bot','--out',bot,'--config',linked)
        botid=h.cli('identity','whoami','--config',bot)['id']
        assert h.cli('note','get','--config',bot)['revision']==note['revision']
        assert h.cli('auth','password','status','--config',bot,expect_ok=False)['error']
        r=probe(b,None,'/api/v1/auth/password',headers={'Authorization':'Bearer '+json.loads(bot.read_text())['token']});assert r['status']==403,r
        h.cli('bot','revoke',botid,'--config',linked)
        assert h.cli('identity','whoami','--config',bot,expect_ok=False)['error']['code']=='principal_inactive'
        echo_at(b,port)
        assert h.cli('identity','whoami','--config',standalone)['id']==sm['principal']['id']
        passed('Member creates/revokes only its own Bot; other clients and network access survive')
        # Hold a TCP stream under the soon-to-be revoked CLI. An independent
        # client linked to the SAME principal must keep its stream/lease alive.
        link2=probe(b,bj,'/api/v1/client-links','POST',{}, {'X-SLB-CSRF':bm['csrf']})['data']
        linked2,lm2=cfg_login(b,'linked-second',link2['code'])
        fp2,port2,_=h.mapping(linked2,'server-echo');echo_at(b,port2)
        if isolated:
            holdfile=b/'hold-result.json'
            code='import socket,time,json,pathlib; s=socket.create_connection(("127.0.0.1",'+str(port)+'),timeout=5);s.settimeout(7);s.sendall(b"hold");assert s.recv(4)==b"hold";pathlib.Path('+repr(str(b/'hold-ready'))+').touch();closed=False\ntry:\n closed=s.recv(1)==b""\nexcept ConnectionResetError: closed=True\npathlib.Path('+repr(str(holdfile))+').write_text(json.dumps({"closed":closed}));s.close()'
            holder=h.launch_cmd(['nsenter','--net=/run/netns/'+NS_B,sys.executable,'-c',code],'hold',b)
            wait_for(lambda:(b/'hold-ready').exists())
        else:
            held=socket.create_connection(('127.0.0.1',port),timeout=5);held.settimeout(7);held.sendall(b'hold');assert held.recv(4)==b'hold';sockets.append(held)
        h.cli('client','revoke',lm['client']['id'],'--config',h.admin)
        wait_for(lambda:fp.poll() is not None,timeout=8)
        assert fp.returncode!=0
        if isolated:
            holder.wait(timeout=8);assert holder.returncode==0 and json.loads(holdfile.read_text())['closed']
        else:
            try:assert held.recv(1)==b''
            except ConnectionResetError:pass
        echo_at(b,port2);assert fp2.poll() is None
        assert probe(b,bj,'/api/v1/me')['status']==200
        assert h.cli('identity','whoami','--config',standalone)['role']=='member'
        assert h.states()[br['deviceId']]=='online'
        passed('revoke one CLI credential closes its actual stream/listener, not sibling CLI/browser/Node')
        # Password rotation closes admission, not unrelated previously authorized identities.
        h.cli('auth','password','set','--password-stdin','--config',h.admin,stdin=PASSWORD+' rotated\n')
        assert probe(b,bj,'/api/v1/me')['status']==200
        assert h.cli('identity','whoami','--config',linked2)['role']=='member'
        denied=probe(a,None,'/api/v1/sessions/password','POST',{'password':PASSWORD,'name':'old join secret'})
        assert denied['status']==403,denied
        passed('joining password rotation affects new admissions, not existing independent credentials')
        h.server.kill();h.server.wait(timeout=6);h.start_server()
        assert probe(b,bj,'/api/v1/me')['status']==401  # sessions are runtime state
        r=probe(b,bj,'/api/v1/sessions/password','POST',{'password':PASSWORD+' rotated','name':'ignored rename'})
        assert r['status']==200,r
        resumed=probe(b,bj,'/api/v1/me')['data'];assert resumed['id']==bm['id']
        restored=h.cli('note','get','--config',linked2);assert restored['revision']==note['revision'] and restored['source']['clientId']==lm['client']['id']
        wait_for(lambda:h.status()['onlineDevices']==2,timeout=15)
        passed('Server crash/restart retains credentials and exact note provenance; session requires sign-in')
        h.server.terminate();h.server.wait(timeout=6)
        integrity=h.cli('storage','check','--data',h.data,'--hash-files');assert integrity['ok']
        passed('post-fault SQLite integrity and attachment metadata verification')
        return {'status':'PASS','count':len(checks),'checks':checks,'realProcesses':True,'twoNetworkNamespaces':isolated,'physicalMacWindows':False,'sharedKernelAndFilesystem':isolated}
    finally:
        for s in sockets:s.close()
        for f in fixtures:f.shutdown();f.server_close()
        h.close()

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--binary',default=ROOT/'bin/seven-air-link');p.add_argument('--isolated',action='store_true');a=p.parse_args()
    out=ROOT/'reports'/('identity-two-linux.json' if a.isolated else 'identity-e2e.json');out.parent.mkdir(exist_ok=True)
    try:r=suite(a.binary,a.isolated)
    except Exception as e:
        out.write_text(json.dumps({'status':'FAIL','error':type(e).__name__+': '+str(e)},ensure_ascii=False,indent=2)+'\n');raise
    out.write_text(json.dumps(r,ensure_ascii=False,indent=2)+'\n');print(json.dumps(r,ensure_ascii=False))
