#!/usr/bin/env python3
"""Two REAL independent loopback/network stacks with actual Go processes.
Run via test_two_linux.sh. Same kernel and filesystem; not two VMs or Mac/Windows.
No credential, private body or production data is written to the report.
"""
from __future__ import annotations
import argparse, concurrent.futures, hashlib, json, os, signal, socket, ssl, subprocess, sys, time, traceback, urllib.request
from pathlib import Path
from e2e import Harness, wait_for
from e2e_m2 import NetworkHarness, free_port, recv_exact
ROOT=Path(__file__).resolve().parents[1]
NS_B='slb-m4-b'
class IsolatedHarness(NetworkHarness):
    def __init__(self,binary):
        super().__init__(binary)
        self.heartbeat,self.offline='5s','15s'  # actual defaults for silence detection
        self.env_a=self.root/'environment-a';self.env_b=self.root/'environment-b'
        for path in (self.env_a,self.env_b):path.mkdir(mode=0o700)
        self.data=self.env_a/'server';self.admin=self.data/'admin.json'
    def in_b(self,args):return any(str(x).startswith(str(self.env_b)+'/') for x in args)
    def command(self,args):return (['nsenter','--net=/run/netns/'+NS_B] if self.in_b(args) else [])+[str(self.binary),*map(str,args)]
    def launch(self,args,label):
        args=list(args)
        if args[0]=='serve':
            args[args.index('--listen')+1]=f'0.0.0.0:{self.port}'
            args += ['--proxy-listen',f'0.0.0.0:{self.proxy_port}']
        return self.launch_cmd(self.command(args),label,self.env_b if self.in_b(args) else self.env_a)
    def launch_cmd(self,cmd,label,home):
        self.counter+=1
        out=open(self.root/f'{self.counter}-{label}.stdout','w');err=open(self.root/f'{self.counter}-{label}.stderr','w');self.handles.extend([out,err])
        p=subprocess.Popen(cmd,stdout=out,stderr=err,cwd=home,env={**os.environ,'HOME':str(home)})
        self.processes.append(p);return p
    def cli(self,*args,expect_ok=True,stdin=None,timeout=18):
        home=self.env_b if self.in_b(args) else self.env_a
        p=subprocess.run(self.command([*args,'--output','json']),input=stdin,text=True,capture_output=True,timeout=timeout,cwd=home,env={**os.environ,'HOME':str(home)})
        if expect_ok and p.returncode:raise AssertionError(f'CLI {args[0:2]!r}: {p.stdout} {p.stderr}')
        if not expect_ok:assert p.returncode!=0,'unexpected success'
        return json.loads(p.stdout)
    def pair_env(self,name,env):
        invite=self.cli('pairing','create','--config',self.admin)
        cfg=env/'node.json'
        url=self.url if env==self.env_a else f'https://10.231.0.11:{self.port}'
        r=self.cli('join',url,'--fingerprint',self.owner['fingerprint'],'--name',name,'--code-stdin','--config',cfg,stdin=invite['code']+'\n')
        return cfg,r['deviceId']
    def bot_env(self,name,env,device):
        invite=self.cli('identity','create','--type','bot','--name',name,'--device',device,'--config',self.admin)
        cfg=env/'bot.json'
        url=self.url if env==self.env_a else f'https://10.231.0.11:{self.port}'
        self.cli('identity','bind',url,'--fingerprint',self.owner['fingerprint'],'--code-stdin','--config',cfg,stdin=invite['code']+'\n')
        return cfg,self.cli('identity','whoami','--config',cfg)['id']
    def fixtures(self):
        for name,home,prefix in [('A',self.env_a,[]),('B',self.env_b,['nsenter','--net=/run/netns/'+NS_B])]:
            self.launch_cmd([*prefix,sys.executable,str(ROOT/'scripts/namespace_fixture.py'),'--name',name],f'fixture-{name}',home)
        wait_for(lambda:local_http(8080)['environment']=='A',detail='A loopback fixture')
        wait_for(lambda:subprocess.run(['nsenter','--net=/run/netns/'+NS_B,sys.executable,'-c',"import urllib.request;assert b'B' in urllib.request.urlopen('http://127.0.0.1:8080',timeout=2).read()"],capture_output=True).returncode==0,detail='B loopback fixture')
def local_http(port):
    with urllib.request.urlopen(f'http://127.0.0.1:{port}/health',timeout=5) as r:return json.loads(r.read())
def resources(p):
    path=Path(f'/proc/{p.pid}');status=(path/'status').read_text()
    return {'rssBytes':int(next(x for x in status.splitlines() if x.startswith('VmRSS:')).split()[1])*1024,'fds':len(list((path/'fd').iterdir()))}
def suite(binary,soak):
    h=IsolatedHarness(binary);checks=[];streams=[]
    def record(test,**detail):checks.append({'test':test,'result':'PASS',**detail});print('PASS',test,detail,flush=True)
    try:
        nsa=os.readlink('/proc/self/ns/net');nsb=subprocess.check_output(['nsenter','--net=/run/netns/'+NS_B,'readlink','/proc/self/ns/net'],text=True).strip();assert nsa!=nsb
        h.fixtures();h.start_server()
        ac,ad=h.pair_env('Linux-A',h.env_a);bc,bd=h.pair_env('Linux-B',h.env_b)
        an,bn=h.node(ac),h.node(bc);wait_for(lambda:h.status()['onlineDevices']==2,timeout=15)
        devices=h.status()['devices'];remote=next(d for d in devices if d['id']==bd)
        assert '10.231.0.12' in json.dumps(remote),remote
        record('two isolated Linux network stacks and loopbacks, separate HOME/profiles, real veth HTTPS/WSS',namespaceA=nsa,namespaceB=nsb,ipA='10.231.0.11',ipB='10.231.0.12',sharedKernel=True,sharedFilesystem=True)
        web=h.service(bc,'env-b-web',8080,ad,'http');echo=h.service(bc,'env-b-echo',8090,ad);digest=h.service(bc,'env-b-digest',8091,ad)
        wait_for(lambda:all(h.service_state(x['id'])=='online' for x in [web,echo,digest]))
        wp,port,_=h.mapping(ac,'env-b-web');ep,epor,_=h.mapping(ac,'env-b-echo');dp,dpor,_=h.mapping(ac,'env-b-digest')
        assert local_http(8080)['environment']=='A' and local_http(port)['environment']=='B'
        # B service is not exposed on its LAN interface.
        try:
            s=socket.create_connection(('10.231.0.12',8080),timeout=1);s.close();raise AssertionError('B target exposed on LAN')
        except (ConnectionRefusedError,TimeoutError):pass
        record('same 127.0.0.1:8080 on A and B stays isolated; mapped A endpoint reaches B; target is not LAN-listening')
        # Real HTTPS proxy request using Owner ACL, without exposing the application to management origin.
        ctx=ssl.create_default_context(cafile=str(h.data/'server.crt'))
        req=urllib.request.Request(f'https://localhost:{h.proxy_port}/proxy/{web["id"]}/health',headers={'Authorization':'Bearer '+h.owner['token']})
        with urllib.request.urlopen(req,context=ctx,timeout=8) as r:assert json.loads(r.read())['environment']=='B'
        record('independent HTTPS reverse-proxy origin traverses B data channel')
        a,aid=h.bot_env('Bot-A',h.env_a,ad);b,bid=h.bot_env('Bot-B',h.env_b,bd)
        assert h.cli('proxy','open','env-b-echo','--config',a,expect_ok=False)['error']['code']=='access_denied'
        h.cli('services','grants','env-b-echo','--allow-devices',ad,'--allow-principals',aid,'--allow-owner','--config',h.admin)
        bp,bpor,_=h.mapping(a,'env-b-echo')
        message=h.cli('message','send','--to',bid,'--text','两个独立 Linux 网络环境 · UTF-8','--idempotency-key','two-env-001','--correlation-id','m4-acceptance','--config',a)['message']
        assert h.cli('message','get',message['id'],'--config',b)['text']==message['text']
        reply=h.cli('message','send','--to',aid,'--text','B 收到，桥接不代表 AI 自动执行','--reply-to',message['id'],'--config',b)['message']
        assert h.cli('message','get',reply['id'],'--config',a)['senderId']==bid
        assert h.cli('message','get',message['id'],'--config',h.admin,expect_ok=False)['error']['code']=='not_found'
        record('independent Bot identities, explicit service ACL, cross-IP bidirectional messages and Owner private-message denial')
        cursor=h.env_b/'inbox.cursor'
        watch_args=['message','watch','--with',aid,'--incoming-only','--cursor-file',cursor,'--timeout','4s','--max-messages','1','--config',b,'--output','ndjson']
        p=subprocess.run(h.command(watch_args),capture_output=True,text=True,timeout=8);assert p.returncode==0 and json.loads(p.stdout)['id']==message['id']
        # Next watch is genuinely live, cursor already consumed historical inbound message.
        watcher=h.launch(watch_args,'watch');wait_for(lambda:next(x for x in h.cli('peers','list','--config',h.admin)['peers'] if x['id']==bid)['listening']>0)
        live=h.cli('message','send','--to',bid,'--text','跨 IP SSE 实时通知','--config',a)['message'];watcher.wait(timeout=7);assert watcher.returncode==0
        wout=h.root/f'{h.counter}-watch.stdout';assert json.loads(wout.read_text())['id']==live['id']
        record('cross-IP live SSE inbox with durable scoped cursor and incoming-only filter')
        data=os.urandom(2*1024*1024)+b'cross-ns';src=h.env_a/'input.bin';src.write_bytes(data)
        f=h.cli('file','send',src,'--to',bid,'--config',a);dst=h.env_b/'received.bin';h.cli('file','get',f['id'],'--out',dst,'--config',b);assert dst.read_bytes()==data
        record('2 MiB+ file between independent profiles; SHA-256 exact',bytes=len(data))
        def one(_):
            payload=os.urandom(512*1024)
            with socket.create_connection(('127.0.0.1',dpor),timeout=15) as s:
                s.sendall(payload);s.shutdown(socket.SHUT_WR);assert recv_exact(s,32)==hashlib.sha256(payload).digest();assert s.recv(1)==b''
        for count in (20,50):
            started=time.monotonic()
            with concurrent.futures.ThreadPoolExecutor(max_workers=count) as pool:list(pool.map(one,range(count)))
            record('cross-environment parallel binary transfer and TCP half-close',flows=count,bytesPerFlow=512*1024,elapsedSeconds=round(time.monotonic()-started,3))
        time.sleep(1);baseline=resources(h.server)
        streams=[socket.create_connection(('127.0.0.1',epor),timeout=6) for _ in range(20)]
        start=time.monotonic();rounds=0
        while time.monotonic()-start<soak:
            for i,s in enumerate(streams):
                payload=f'{rounds}:{i}:'.encode()+os.urandom(256);s.sendall(payload);assert recv_exact(s,len(payload))==payload
            assert h.status()['onlineDevices']==2
            rounds+=1;time.sleep(2)
        for s in streams:s.close()
        streams.clear();time.sleep(1)
        record('bounded sustained cross-IP workload; not a long-duration stability certification',flows=20,rounds=rounds,elapsedSeconds=round(time.monotonic()-start,3),serverBefore=baseline,serverAfter=resources(h.server))
        # A stays connected while B loses its actual veth interface (not just stopping the process).
        old=socket.create_connection(('127.0.0.1',epor),timeout=5);streams.append(old);old.sendall(b'before');assert recv_exact(old,6)==b'before'
        started=time.monotonic();subprocess.run(['ip','-n',NS_B,'link','set','slb-vb','down'],check=True)
        wait_for(lambda:h.states()[bd]=='offline',timeout=21,detail='veth-down detection at 5s/15s');assert h.states()[ad]=='online';elapsed=time.monotonic()-started
        assert h.cli('message','send','--to',bid,'--text','B 网络断开时持久保存','--config',a)['message']['status']=='accepted'
        subprocess.run(['ip','-n',NS_B,'link','set','slb-vb','up'],check=True)
        wait_for(lambda:h.states()[bd]=='online',timeout=25,detail='veth restored reconnect')
        wait_for(lambda:local_http(port)['environment']=='B',timeout=15)
        old.settimeout(2)
        try:assert old.recv(1)==b''
        except (ConnectionResetError,ConnectionAbortedError):pass
        old.close();streams.clear()
        record('actual link-down isolates B, detects offline, A survives, old flow closes, same mapped port recovers',offlineSeconds=round(elapsed,3))
        durable=h.cli('message','send','--to',bid,'--text','SIGKILL 前已确认持久化','--idempotency-key','durable-m4','--config',a)['message']
        started=time.monotonic();h.server.kill();h.server.wait(timeout=3);time.sleep(.3);h.start_server()
        wait_for(lambda:h.status()['onlineDevices']==2,timeout=20);wait_for(lambda:local_http(port)['environment']=='B',timeout=20)
        assert h.cli('message','get',durable['id'],'--config',b)['text']==durable['text']
        assert h.cli('message','send','--to',bid,'--text',durable['text'],'--idempotency-key','durable-m4','--config',a)['duplicate']
        h.cli('file','get',f['id'],'--out',h.env_b/'after-kill.bin','--config',b);assert (h.env_b/'after-kill.bin').read_bytes()==data
        record('Server SIGKILL with SQLite WAL; identities, accepted message, idempotency, files and same-port mapping recover',elapsedSeconds=round(time.monotonic()-started,3))
        # Each forward independently renews every 3s. HTTP readiness is not Bot readiness.
        def bot_mapping_ready():
            try:
                with socket.create_connection(('127.0.0.1',bpor),timeout=3) as conn:
                    conn.sendall(b'ready');return recv_exact(conn,5)==b'ready'
            except (OSError,AssertionError):return False
        wait_for(bot_mapping_ready,timeout=12,detail='Bot mapping independently recreates its lease')
        # Mapping was recreated after Server boot; credentials are still checked.
        s=socket.create_connection(('127.0.0.1',bpor),timeout=5);s.sendall(b'bot');assert recv_exact(s,3)==b'bot'
        h.cli('bot','revoke',aid,'--config',h.admin)
        s.settimeout(3)
        try:assert s.recv(1)==b''
        except (ConnectionResetError,ConnectionAbortedError):pass
        s.close();wait_for(lambda:bp.poll() is not None,timeout=7);assert h.status()['onlineDevices']==2
        record('Bot revoke terminates established flow and frontend listener without revoking Nodes')
        h.cli('devices','revoke',bd,'--config',h.admin);wait_for(lambda:bn.poll() is not None,timeout=7)
        assert h.cli('identity','whoami','--config',b,expect_ok=False)['error']['code']=='principal_inactive'
        assert h.states()[ad]=='online'
        record('device revoke stops remote Node, invalidates associated identity, leaves A authorized')
        assert h.cli('doctor','--config',h.admin)['checks'][0]['result']=='pass'
        h.server.terminate();h.server.wait(timeout=6)
        integrity=h.cli('storage','check','--data',h.data,'--hash-files');assert integrity['ok'] and integrity['integrity']=='ok'
        record('offline post-fault SQLite integrity and every active attachment hash verified',sqliteVersion=integrity['sqliteVersion'],messages=integrity['messages'])
        return {'status':'PASS','environment':'two Linux network namespaces connected by isolated veth; same host kernel/filesystem','checks':checks,'physicalMachines':False,'macWindowsTested':False,'soakRequestedSeconds':soak}
    except Exception:
        # Capture diagnostic tails for failures only; never put profile contents into logs.
        for path in h.root.glob('*.stderr'):
            content=path.read_text(errors='replace')[-2000:]
            if content:print(path.name,content,file=sys.stderr)
        raise
    finally:
        subprocess.run(['ip','-n',NS_B,'link','set','slb-vb','up'],capture_output=True)
        for s in streams:s.close()
        h.close()
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--binary',default=ROOT/'bin/seven-air-link');p.add_argument('--soak-seconds',type=int,default=60);a=p.parse_args()
    if a.soak_seconds<10:raise SystemExit('soak must be at least 10 seconds')
    target=ROOT/'reports/two-linux-environments.json'
    try:r=suite(a.binary,a.soak_seconds)
    except Exception as e:
        target.write_text(json.dumps({'status':'FAIL','error':str(e)},ensure_ascii=False,indent=2)+'\n');raise
    target.write_text(json.dumps(r,ensure_ascii=False,indent=2)+'\n')
