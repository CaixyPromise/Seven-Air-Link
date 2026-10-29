#!/usr/bin/env python3
"""M2 process/socket acceptance. Requires Python 3; websocket test uses websockets.
Every fixture uses real loopback TCP/TLS, separate CLI/Server/Node processes.
This is not Mac/Windows or physical-LAN acceptance.
"""
from __future__ import annotations
import argparse, concurrent.futures, hashlib, http.server, json, os, signal, socket, socketserver, ssl, threading, time, urllib.error, urllib.request
from pathlib import Path
from e2e import Harness, wait_for
ROOT=Path(__file__).resolve().parents[1]
def free_port():
    with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]
def recv_exact(sock,n):
    out=bytearray()
    while len(out)<n:
        b=sock.recv(n-len(out))
        if not b:raise AssertionError(f'premature EOF: {len(out)}/{n}')
        out.extend(b)
    return bytes(out)
class NetworkHarness(Harness):
    def __init__(self,binary):
        super().__init__(binary,heartbeat='1s',offline='4s');self.proxy_port=free_port()
    def launch(self,args,label):
        if args[0]=='serve':args=[*args,'--proxy-listen',f'127.0.0.1:{self.proxy_port}']
        return super().launch(args,label)
    def service(self,cfg,name,port,source,protocol='tcp'):
        return self.cli('share',protocol,'--name',name,'--target',f'127.0.0.1:{port}','--allow-devices',source,'--allow-owner','--config',cfg)
    def mapping(self,cfg,name):
        port=free_port();p=self.launch(['proxy','open',name,'--listen',f'127.0.0.1:{port}','--config',cfg],'forward')
        def ready():
            assert p.poll() is None,'forward process exited before readiness'
            f=self.cli('proxy','list','--config',cfg)['forwards']
            return next((x for x in f if x['listen']==f'127.0.0.1:{port}'),None)
        lease=wait_for(ready,detail='forward lease registration')
        return p,port,lease
    def service_state(self,id):
        services=self.cli('services','list','--config',self.admin)['services']
        return next(x['status'] for x in services if x['id']==id)
class ThreadedTCP(socketserver.ThreadingTCPServer):
    allow_reuse_address=True;daemon_threads=True
class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(30)
        try:
            while b:=self.request.recv(65536):self.request.sendall(b)
            self.request.shutdown(socket.SHUT_WR)
        except (OSError,TimeoutError):pass
class Digest(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(30);h=hashlib.sha256()
        try:
            while b:=self.request.recv(65536):h.update(b)
            self.request.sendall(h.digest());self.request.shutdown(socket.SHUT_WR)
        except (OSError,TimeoutError):pass
class Flood(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(30)
        try:
            while True:self.request.sendall(b'F'*65536)
        except (OSError,TimeoutError):pass
class HTTP(http.server.BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*_):pass
    def do_GET(self):
        if self.path=='/redirect':
            self.send_response(302);self.send_header('Location','/health');self.send_header('Content-Length','0');self.end_headers();return
        if self.path=='/events':
            self.send_response(200);self.send_header('Content-Type','text/event-stream');self.send_header('Connection','close');self.end_headers()
            self.wfile.write(b'data: first\n\n');self.wfile.flush();time.sleep(.6);self.wfile.write(b'data: last\n\n');self.close_connection=True;return
        obj={'ok':True,'text':'跨设备开发 · 中文正常','path':self.path,'authorization':self.headers.get('Authorization'),'cookie':self.headers.get('Cookie'),'bridge':self.headers.get('X-SLB-Secret')}
        data=json.dumps(obj,ensure_ascii=False).encode();self.send_response(200);self.send_header('Content-Type','application/json; charset=utf-8');self.send_header('Set-Cookie','slb_session=evil; Path=/');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
    def do_POST(self):
        size=int(self.headers['Content-Length']);h=hashlib.sha256()
        while size:
            b=self.rfile.read(min(size,65536));assert b;size-=len(b);h.update(b)
        data=json.dumps({'sha256':h.hexdigest()}).encode();self.send_response(200);self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
def spawn_server(server):threading.Thread(target=server.serve_forever,daemon=True).start();return server

def suite(binary):
    h=NetworkHarness(binary);results=[];fixtures=[];extra=[]
    def record(name,**detail):
        results.append({'test':name,'result':'PASS',**detail});print('PASS',name,detail,flush=True)
    try:
        h.start_server();a,aid=h.pair('Source · Linux process','source');b,bid=h.pair('Target · Linux process','target');other,oid=h.pair('Unauthorized device','other')
        an,bn=h.node(a),h.node(b);wait_for(lambda:h.status()['onlineDevices']==2)
        echo=spawn_server(ThreadedTCP(('127.0.0.1',0),Echo));digest=spawn_server(ThreadedTCP(('127.0.0.1',0),Digest));flood=spawn_server(ThreadedTCP(('127.0.0.1',0),Flood));websrv=spawn_server(http.server.ThreadingHTTPServer(('127.0.0.1',0),HTTP))
        fixtures += [echo,digest,flood,websrv]
        es=h.service(b,'echo',echo.server_address[1],aid);ds=h.service(b,'digest',digest.server_address[1],aid);hs=h.service(b,'web',websrv.server_port,aid,'http');fs=h.service(b,'flood',flood.server_address[1],aid)
        wait_for(lambda:all(h.service_state(s['id'])=='online' for s in [es,ds,hs,fs]))
        record('real Server + 2 Nodes; local-only approval and registered service readiness')
        ep,eport,ef=h.mapping(a,'echo');dp,dport,df=h.mapping(a,'digest');hp,hport,hf=h.mapping(a,'web')
        def echo_roundtrip(port=eport):
            with socket.create_connection(('127.0.0.1',port),timeout=8) as s:
                payload=b'not-for-audit:'+os.urandom(8192);s.sendall(payload);assert recv_exact(s,len(payload))==payload
            return True
        echo_roundtrip()
        response=urllib.request.urlopen(f'http://127.0.0.1:{hport}/health',timeout=8)
        assert json.loads(response.read())['text']=='跨设备开发 · 中文正常'
        record('local TCP mapping and UTF-8 HTTP access without exposing target listening address')
        payload=os.urandom(8*1024*1024)
        req=urllib.request.Request(f'http://127.0.0.1:{hport}/upload',data=payload,method='POST')
        assert json.loads(urllib.request.urlopen(req,timeout=20).read())['sha256']==hashlib.sha256(payload).hexdigest()
        record('8 MiB HTTP upload over TCP mapping; SHA-256 exact')
        def digest_one(_):
            payload=os.urandom(512*1024)
            with socket.create_connection(('127.0.0.1',dport),timeout=20) as s:
                s.settimeout(25);s.sendall(payload);s.shutdown(socket.SHUT_WR)
                assert recv_exact(s,32)==hashlib.sha256(payload).digest();assert s.recv(1)==b''
        for count in (20,50):
            began=time.monotonic()
            with concurrent.futures.ThreadPoolExecutor(max_workers=count) as pool:list(pool.map(digest_one,range(count)))
            record(f'{count} concurrent TCP streams; FIN, random binary and SHA-256 isolation',streams=count,bytesPerStream=512*1024,elapsedSeconds=round(time.monotonic()-began,3))
        # Keep 20 simultaneous streams open across several real heartbeats.
        persistent=[socket.create_connection(('127.0.0.1',eport),timeout=8) for _ in range(20)]
        began=time.monotonic()
        try:
            for round_no in range(6):
                for index,conn in enumerate(persistent):
                    value=f"stream={index};round={round_no};".encode()+os.urandom(512)
                    conn.sendall(value);assert recv_exact(conn,len(value))==value
                assert h.status()['onlineDevices']==2
                time.sleep(2)
        finally:
            for conn in persistent:conn.close()
        record('20 persistent simultaneous TCP streams across heartbeats; periodic byte isolation',elapsedSeconds=round(time.monotonic()-began,3),rounds=6)
        # A stalled consumer must not starve the control channel or independent flows.
        fp,fport,ff=h.mapping(a,'flood')
        slow=socket.create_connection(('127.0.0.1',fport),timeout=8);slow.setsockopt(socket.SOL_SOCKET,socket.SO_RCVBUF,4096);time.sleep(1.2)
        echo_roundtrip();assert h.status()['onlineDevices']==2;slow.close()
        h.cli('proxy','close',ff['id'],'--config',a);wait_for(lambda:fp.poll() is not None,detail='explicit forward close')
        record('slow consumer backpressure preserves other flows and control heartbeat; explicit stop exits CLI')
        sslctx=ssl.create_default_context(cafile=str(h.data/'server.crt'))
        target_ssl=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);target_ssl.load_cert_chain(str(h.data/'server.crt'),str(h.data/'server.key'))
        class TLSDigest(ThreadedTCP):
            def get_request(self):
                conn,addr=super().get_request();return target_ssl.wrap_socket(conn,server_side=True),addr
        tlsserver=spawn_server(TLSDigest(('127.0.0.1',0),Echo));fixtures.append(tlsserver)
        ts=h.service(b,'tls',tlsserver.server_address[1],aid);wait_for(lambda:h.service_state(ts['id'])=='online');tp,tport,tf=h.mapping(a,'tls')
        with socket.create_connection(('127.0.0.1',tport),timeout=8) as raw:
            with sslctx.wrap_socket(raw,server_hostname='localhost') as s:
                data=os.urandom(32768);s.sendall(data);assert recv_exact(s,len(data))==data
        record('TLS-over-TCP passes through unchanged and certificate validation succeeds')
        gateway=f'https://localhost:{h.proxy_port}/proxy/{hs["id"]}'
        def gateway_request(path):
            req=urllib.request.Request(gateway+path,headers={'Authorization':'Bearer '+h.owner['token'],'Cookie':'slb_session=secret','X-SLB-Secret':'hidden'})
            return urllib.request.urlopen(req,context=sslctx,timeout=8)
        with gateway_request('/health') as response:
            data=json.loads(response.read());assert data['ok'] and not data['authorization'] and not data['cookie'] and not data['bridge'];assert response.headers.get('Set-Cookie') is None
        with gateway_request('/redirect') as response:assert json.loads(response.read())['path']=='/health'
        began=time.monotonic()
        with gateway_request('/events') as response:
            assert response.readline()==b'data: first\n';first=time.monotonic()-began;assert first<.5;assert b'data: last' in response.read()
        record('isolated HTTP gateway: credential stripping, redirect rewrite and unbuffered SSE',firstEventSeconds=round(first,3))
        from websockets.sync.server import serve
        from websockets.sync.client import connect
        # Independent RFC6455 client speaks the actual M2 data subprotocol,
        # not just an application WebSocket carried inside a TCP tunnel.
        source_token=json.loads(a.read_text())['token']
        status,ticket=h.request('POST','/api/v1/tunnels',{'forwardId':ef['id']},token=source_token)
        assert status==201
        data_url=h.url.replace('https:','wss:')+'/api/v1/tunnel/data'
        with connect(data_url,ssl=sslctx,subprotocols=['slb.data.v1'],compression=None,proxy=None,additional_headers={'Authorization':'Bearer '+source_token,'X-SLB-Tunnel-ID':ticket['tunnelId'],'X-SLB-Tunnel-Ticket':ticket['ticket']}) as ws:
            assert ws.recv(timeout=5)==b'\x00'
            value=os.urandom(100000)
            for offset in range(0,len(value),16000):
                part=value[offset:offset+16000];ws.send([b'\x01'+part[:20],part[20:]])
            ws.send(b'\x02')
            received=bytearray()
            while True:
                frame=ws.recv(timeout=5)
                if frame==b'\x02':break
                assert frame[0]==1 and len(frame)<=16384;received.extend(frame[1:])
            assert received==value
        record('independent Python client speaks slb.data.v1: READY, fragmented DATA and bidirectional FIN')
        def ws_echo(ws):
            for message in ws:ws.send(message)
        wsserver=serve(ws_echo,'127.0.0.1',0,max_size=2*1024*1024,compression=None);threading.Thread(target=wsserver.serve_forever,daemon=True).start();extra.append(wsserver)
        wsport=wsserver.socket.getsockname()[1];wsvc=h.service(b,'websocket',wsport,aid,'http');wait_for(lambda:h.service_state(wsvc['id'])=='online')
        url=f'wss://localhost:{h.proxy_port}/proxy/{wsvc["id"]}/socket'
        with connect(url,ssl=sslctx,additional_headers={'Authorization':'Bearer '+h.owner['token']},open_timeout=8,max_size=2*1024*1024,compression=None,proxy=None) as ws:
            for data in ('WebSocket · 实时',os.urandom(1024*1024)):
                ws.send(data);assert ws.recv(timeout=8)==data
        record('WebSocket Upgrade through HTTP gateway; independent Python implementation; text + 1 MiB binary')
        error=h.cli('proxy','open','echo','--listen','127.0.0.1:0','--config',other,expect_ok=False);assert error['error']['code']=='access_denied'
        error=h.cli('proxy','open','echo','--listen','0.0.0.0:18080','--config',a,expect_ok=False);assert error['error']['code']=='unsafe_listen'
        error=h.cli('share','tcp','--name','unsafe','--target','192.168.1.1:22','--config',b,expect_ok=False);assert error['error']
        error=h.cli('proxy','open','echo','--listen',f'127.0.0.1:{eport}','--config',a,expect_ok=False);assert error['error']['code']=='listen_port_in_use'
        record('unauthorized device, non-loopback target/listener and occupied listener denied')
        # Revoke a live flow, then validate that a retry/reconnect cannot restore stale grants.
        existing=socket.create_connection(('127.0.0.1',eport),timeout=8);existing.sendall(b'before');assert recv_exact(existing,6)==b'before'
        h.cli('services','grants','echo','--allow-owner','--config',h.admin)
        existing.settimeout(3)
        try:assert existing.recv(1)==b''
        except ConnectionResetError:pass
        existing.close();wait_for(lambda:ep.poll() is not None,detail='revoked forward process exit')
        error=h.cli('proxy','open','echo','--config',a,expect_ok=False);assert error['error']['code']=='access_denied'
        h.cli('services','grants','echo','--allow-devices',aid,'--allow-owner','--config',h.admin)
        ep,eport,ef=h.mapping(a,'echo');echo_roundtrip(eport)
        record('ACL edit closes existing streams and rejects new flows; explicit regrant restores access')
        existing=socket.create_connection(('127.0.0.1',eport),timeout=8);existing.sendall(b'old');assert recv_exact(existing,3)==b'old'
        bn.terminate();bn.wait(timeout=6);wait_for(lambda:h.service_state(es['id'])=='offline')
        existing.settimeout(2)
        try:assert existing.recv(1)==b''
        except ConnectionResetError:pass
        existing.close()
        began=time.monotonic()
        with socket.create_connection(('127.0.0.1',eport),timeout=2) as conn:
            conn.settimeout(2)
            try:assert conn.recv(1)==b''
            except ConnectionResetError:pass
        assert time.monotonic()-began<2
        bn=h.node(b);wait_for(lambda:h.service_state(es['id'])=='online');wait_for(lambda:echo_roundtrip(eport),detail='same port recovery after Node restart')
        record('target Node disconnect closes streams; known-offline fails within 2s; same local port recovers')
        # Server kill must not pretend that the old TCP connection survived.
        old=socket.create_connection(('127.0.0.1',eport),timeout=8);old.sendall(b'old');assert recv_exact(old,3)==b'old'
        h.server.kill();h.server.wait(timeout=4);old.settimeout(3)
        try:assert old.recv(1)==b''
        except ConnectionResetError:pass
        old.close();began=time.monotonic();h.start_server()
        wait_for(lambda:h.status()['onlineDevices']==2,timeout=15)
        wait_for(lambda:echo_roundtrip(eport),timeout=20,detail='forward lease re-created after Server restart')
        assert ep.poll() is None
        record('Server SIGKILL/restart: authorization persistent, Nodes reconnect, CLI recreates lease at same port',recoverySeconds=round(time.monotonic()-began,3))
        h.cli('services','stop','echo','--config',h.admin)
        bn.terminate();bn.wait(timeout=5);bn=h.node(b);wait_for(lambda:h.status()['onlineDevices']==2)
        time.sleep(.4);assert h.service_state(es['id'])=='stopped'
        h.cli('share','stop','echo','--config',b)
        replacement=h.service(b,'echo',echo.server_address[1],aid);assert replacement['id']!=es['id']
        wait_for(lambda:h.service_state(replacement['id'])=='online')
        record('stopped service tombstone survives Node restart; explicit local re-share obtains new service identity')
        audit=h.request('GET','/api/v1/activity?limit=500')[1]
        raw=json.dumps(audit)
        for secret in (h.owner['token'],json.loads(a.read_text())['token'],json.loads(b.read_text())['token'],'not-for-audit:'):
            assert secret not in raw
        record('audit contains operation metadata, not credentials or TCP payload')
        # Process snapshots are descriptive, not a formal memory-leak benchmark.
        try:
            import psutil
            resources=[]
            for role,p in [('server',h.server),('source-node',an),('target-node',bn)]:
                proc=psutil.Process(p.pid);resources.append({'role':role,'rssBytes':proc.memory_info().rss,'fds':proc.num_fds()})
            record('post-workload process resource snapshot',processes=resources)
        except ImportError:resources=[]
        return results
    except Exception:
        # Print diagnostics only. Generated secrets live in deleted temp profiles,
        # so never dump arbitrary CLI stdout or configuration files into reports.
        for path in h.root.glob('*.stderr'):
            print('DIAGNOSTIC',path.name,path.read_text()[-5000:],flush=True)
        raise
    finally:
        h.close()
        for server in extra:server.shutdown()
        for server in fixtures:server.shutdown();server.server_close()

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--binary',default=str(ROOT/'bin/seven-air-link'));args=parser.parse_args()
    out=ROOT/'reports/m2-e2e.json'
    try:
        results=suite(args.binary);result={'status':'PASS','environment':'Linux real processes / loopback only; not physical LAN or native Mac/Windows','tests':results}
    except Exception as exc:
        result={'status':'FAIL','error':repr(exc)};out.write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n');raise
    out.write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
if __name__=='__main__':main()
