#!/usr/bin/env python3
"""Actual M2 binary -> M3 binary upgrade test. Never uses the user's real state."""
import argparse,hashlib,json,socket
from pathlib import Path
from e2e import ROOT,wait_for
from e2e_m2 import NetworkHarness,ThreadedTCP,Echo,spawn_server,recv_exact

def run(old,new):
    h=NetworkHarness(old);echo=None
    try:
        assert h.cli('version')['version']=='0.2.0-m2'
        h.start_server();a,aid=h.pair('M2 source','source');b,bid=h.pair('M2 target','target');an,bn=h.node(a),h.node(b)
        wait_for(lambda:h.status()['onlineDevices']==2)
        echo=spawn_server(ThreadedTCP(('127.0.0.1',0),Echo));svc=h.service(b,'migrated-echo',echo.server_address[1],aid)
        wait_for(lambda:h.service_state(svc['id'])=='online');mapping,port,_=h.mapping(a,'migrated-echo')
        with socket.create_connection(('127.0.0.1',port),timeout=5) as conn:conn.sendall(b'M2');assert recv_exact(conn,2)==b'M2'
        for p in [mapping,an,bn,h.server]:p.terminate();p.wait(timeout=8)
        before=json.loads((h.data/'metadata.json').read_text());assert before['version']==2
        admin_hash=hashlib.sha256(h.admin.read_bytes()).hexdigest();a_bytes=a.read_bytes();b_bytes=b.read_bytes()
        h.binary=Path(new).resolve();h.start_server();h.node(a);h.node(b);wait_for(lambda:h.status()['onlineDevices']==2)
        assert h.cli('version')['version']=='0.3.0-m3'
        after=json.loads((h.data/'metadata.json').read_text());backup=json.loads((h.data/'metadata.json.v2.bak').read_text())
        assert after['version']==3 and backup['version']==2
        assert before['ownerTokenHash']==after['ownerTokenHash'] and before['services']==after['services']
        assert admin_hash==hashlib.sha256(h.admin.read_bytes()).hexdigest() and a_bytes==a.read_bytes() and b_bytes==b.read_bytes()
        mapping,port,_=h.mapping(a,'migrated-echo')
        with socket.create_connection(('127.0.0.1',port),timeout=5) as conn:conn.sendall(b'M3');assert recv_exact(conn,2)==b'M3'
        cfg=h.root/'bot.json';newbot=h.cli('bot','bind','--name','Upgraded Bot','--out',cfg,'--config',h.admin)
        sent=h.cli('message','send','--to','shared','--text','new collaboration on upgraded network','--config',cfg)
        assert h.cli('message','get',sent['message']['id'],'--config',h.admin)['senderId']==newbot['principal']['id']
        return {'status':'PASS','oldBinarySha256':hashlib.sha256(Path(old).read_bytes()).hexdigest(),'newBinarySha256':hashlib.sha256(Path(new).read_bytes()).hexdigest(),'checks':['actual published M2 startup and TCP path','schema2->3 backup','same Owner/Device credentials and certificates','same Service IDs/ACLs/local approval','old configs reused without pairing','M3 TCP mapping after migration','M3 Bot message on migrated state'],'environment':'Linux amd64; separate real Server/Nodes and CLI processes'}
    finally:
        h.close()
        if echo:echo.shutdown();echo.server_close()
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--old-binary',required=True);p.add_argument('--new-binary',default=ROOT/'bin/seven-air-link');a=p.parse_args()
    try:report=run(a.old_binary,a.new_binary)
    except Exception as exc:
        (ROOT/'reports/migration-m3.json').write_text(json.dumps({'status':'FAIL','error':str(exc)},indent=2)+'\n');raise
    (ROOT/'reports/migration-m3.json').write_text(json.dumps(report,indent=2)+'\n');print(json.dumps(report))
