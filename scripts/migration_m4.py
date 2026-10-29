#!/usr/bin/env python3
"""Published M3 -> SQLite M4 -> verified restore and real M3 point-in-time rollback.
Only disposable local test data. No production profiles are touched.
"""
from __future__ import annotations
import argparse, hashlib, json, os, shutil, socket, sqlite3, time
from pathlib import Path
from e2e import ROOT,wait_for
from e2e_m2 import NetworkHarness,ThreadedTCP,Echo,spawn_server,recv_exact

def run(old,new):
    h=NetworkHarness(old);echo=None;results=[];original=Path(old).resolve();new=Path(new).resolve()
    def record(test,**detail):results.append({'test':test,'result':'PASS',**detail});print('PASS',test,detail,flush=True)
    def stop_all():
        for p in reversed(h.processes):
            if p.poll() is None:p.terminate()
        for p in h.processes:
            try:p.wait(timeout=8)
            except Exception:p.kill();p.wait(timeout=3)
    def echo_once(port,data):
        with socket.create_connection(('127.0.0.1',port),timeout=5) as s:s.sendall(data);assert recv_exact(s,len(data))==data
    def node_up():
        h.node(na);h.node(nb);wait_for(lambda:h.status()['onlineDevices']==2,timeout=15)
    try:
        assert h.cli('version')['version']=='0.3.0-m3'
        h.start_server();na,da=h.pair('Migration-A','a');nb,db=h.pair('Migration-B','b');node_up()
        a=h.root/'bot-a.json';b=h.root/'bot-b.json';aid=h.cli('bot','bind','--name','Existing Bot A','--out',a,'--config',h.admin)['principal']['id'];bid=h.cli('bot','bind','--name','Existing Bot B','--out',b,'--config',h.admin)['principal']['id']
        msg=h.cli('message','send','--to',bid,'--text','M3 私信，迁移后保持原认证来源。','--idempotency-key','migration-001','--correlation-id','upgrade-check','--config',a)['message']
        reply=h.cli('message','send','--to',aid,'--text','回复链保持','--reply-to',msg['id'],'--config',b)['message']
        raw=os.urandom(1024*1024)+'历史附件'.encode();src=h.root/'payload.bin';src.write_bytes(raw)
        file=h.cli('file','send',src,'--to',bid,'--config',a)
        linked=h.cli('message','send','--to',bid,'--text','历史附件引用','--attach',file['id'],'--config',a)['message']
        echo=spawn_server(ThreadedTCP(('127.0.0.1',0),Echo));svc=h.service(nb,'migration-echo',echo.server_address[1],da);wait_for(lambda:h.service_state(svc['id'])=='online')
        mapping,port,_=h.mapping(na,'migration-echo');echo_once(port,b'M3 actual service')
        stop_all();before=(h.data/'metadata.json').read_bytes();source_sha=hashlib.sha256(before).hexdigest();pre=json.loads(before)
        authority_hash={n:hashlib.sha256((h.data/n).read_bytes()).hexdigest() for n in ('admin.json','server.crt','server.key')}
        record('published M3 creates actual identities, private messages/replies, attachment and approved TCP service',oldBinarySHA256=hashlib.sha256(original.read_bytes()).hexdigest())
        h.binary=new;pre_backup=h.root/'before-m4-backup'
        result=h.cli('storage','backup','--data',h.data,'--out',pre_backup);assert result['ok'] and result['schema']==3
        assert h.cli('storage','verify','--from',pre_backup)['ok']
        assert (pre_backup/'metadata.json').read_bytes()==before
        record('offline full pre-upgrade M3 backup includes authority and hashes; original JSON byte-exact')
        result=h.cli('storage','migrate','--data',h.data)
        assert result['sourceSHA256']==source_sha and result['sourceVersion']==3
        assert (h.data/'metadata.json.pre-sqlite.bak').read_bytes()==before and json.loads((h.data/'metadata.json').read_text())['version']==4
        with sqlite3.connect(h.data/'bridge.db') as sql:
            assert sql.execute('PRAGMA user_version').fetchone()[0]==4
            assert sql.execute('PRAGMA journal_mode').fetchone()[0]=='wal'
            assert sql.execute('SELECT count(*) FROM messages').fetchone()[0]==len(pre['messages'])
            assert sql.execute('SELECT source_sha256 FROM migrations').fetchone()[0]==source_sha
        h.binary=original;h.cli('init','--data',h.data,expect_ok=False);h.binary=new
        assert all(hashlib.sha256((h.data/n).read_bytes()).hexdigest()==digest for n,digest in authority_hash.items())
        record('schema3 JSON -> schema4 indexed SQLite WAL; source seal, backup and old-writer downgrade guard; credentials unchanged')
        h.start_server();node_up();wait_for(lambda:h.service_state(svc['id'])=='online')
        current=h.cli('services','list','--config',h.admin)['services'];assert any(s['id']==svc['id'] and s['allowDevices']==svc['allowDevices'] for s in current)
        _,port,_=h.mapping(na,'migration-echo');echo_once(port,b'M4 after upgrade')
        assert h.cli('message','get',msg['id'],'--config',b)['text']==msg['text']
        assert h.cli('message','get',reply['id'],'--config',a)['replyTo']==msg['id']
        assert h.cli('message','get',linked['id'],'--config',b)['attachments']==[file['id']]
        assert h.cli('message','get',msg['id'],'--config',h.admin,expect_ok=False)['error']['code']=='not_found'
        dup=h.cli('message','send','--to',bid,'--text',msg['text'],'--idempotency-key','migration-001','--correlation-id','upgrade-check','--config',a);assert dup['duplicate'] and dup['message']['id']==msg['id']
        h.cli('file','get',file['id'],'--out',h.root/'after-migration.bin','--config',b);assert (h.root/'after-migration.bin').read_bytes()==raw
        record('existing configs/Service IDs/ACL/local approval reused; private visibility, UTF-8/replies/files and idempotency preserved')
        # Offline maintenance cannot race active API/file writers.
        h.cli('storage','backup','--data',h.data,'--out',h.root/'unsafe-live',expect_ok=False)
        h.cli('storage','gc','--data',h.data,'--apply',expect_ok=False)
        assert not (h.root/'unsafe-live').exists()
        diagnostic=h.cli('doctor','--config',h.admin);assert 'sqlite' in json.dumps(diagnostic)
        h.cli('init','--data',h.data,expect_ok=False)
        record('active Server lock rejects maintenance; authenticated doctor reports actual SQLite backend')
        committed=h.cli('message','send','--to',bid,'--text','M4 commit before crash','--idempotency-key','m4-crash','--config',a)['message']
        h.server.kill();h.server.wait(timeout=3);h.start_server();wait_for(lambda:h.status()['onlineDevices']==2,timeout=15)
        assert h.cli('message','get',committed['id'],'--config',b)['text']==committed['text']
        stop_all()
        check=h.cli('storage','check','--data',h.data,'--hash-files');assert check['ok'] and check['integrity']=='ok'
        record('acknowledged SQLite write survives SIGKILL, restart and full attachment integrity check',sqliteVersion=check['sqliteVersion'])
        backup=h.root/'sqlite-backup';assert h.cli('storage','backup','--data',h.data,'--out',backup)['schema']==4
        assert not (backup/'bridge.db-wal').exists() and not (backup/'bridge.db-shm').exists()
        assert h.cli('storage','verify','--from',backup)['ok']
        corrupt=h.root/'tampered';shutil.copytree(backup,corrupt);(corrupt/'uploads'/file['id']).write_bytes(b'tampered')
        h.cli('storage','verify','--from',corrupt,expect_ok=False)
        h.cli('storage','restore','--from',corrupt,'--data',h.root/'bad-restore',expect_ok=False);assert not (h.root/'bad-restore').exists()
        h.cli('storage','restore','--from',backup,'--data',h.data,expect_ok=False)
        record('standalone VACUUM INTO backup, hash-manifest verification, tamper refusal, existing-target restore refusal')
        original_data=h.data;restored=h.root/'restored-sqlite';assert h.cli('storage','restore','--from',backup,'--data',restored)['ok']
        h.data=restored;h.admin=restored/'admin.json';h.start_server();node_up()
        wait_for(lambda:h.service_state(svc['id'])=='online');_,port,_=h.mapping(na,'migration-echo');echo_once(port,b'restored real TCP')
        assert h.cli('message','get',committed['id'],'--config',b)['text']==committed['text']
        h.cli('file','get',file['id'],'--out',h.root/'restored.bin','--config',b);assert (h.root/'restored.bin').read_bytes()==raw
        stop_all();record('restore to new directory actually runs Server/Nodes, messages, attachment download and TCP forwarding')
        # Safe, repeatable collection: keep active referenced file and new partial; skip symlink.
        uploads=h.data/'uploads';orphan=uploads/('file_'+'o'*24);orphan.write_bytes(b'orphan');partial=uploads/'.upload-stale';partial.write_bytes(b'partial');fresh=uploads/'.upload-new';fresh.write_bytes(b'fresh')
        outside=h.root/'outside.txt';outside.write_text('never delete');link=uploads/('file_'+'z'*24);link.symlink_to(outside)
        stale=time.time()-2*24*3600
        for p in (orphan,partial):os.utime(p,(stale,stale))
        preview=h.cli('storage','gc','--data',h.data);assert not preview['applied'] and preview['removed']==0 and orphan.exists() and partial.exists()
        assert {x['name'] for x in preview['candidates']}=={orphan.name,partial.name}
        applied=h.cli('storage','gc','--data',h.data,'--apply');assert applied['removed']==2
        assert (uploads/file['id']).exists() and fresh.exists() and outside.exists() and link.is_symlink()
        assert h.cli('storage','gc','--data',h.data,'--apply')['removed']==0
        assert h.cli('storage','check','--data',h.data,'--hash-files')['ok']
        saved=(uploads/file['id']).read_bytes();(uploads/file['id']).unlink()
        broken=h.cli('storage','check','--data',h.data,'--hash-files',expect_ok=False);assert 'missing' in json.dumps(broken)
        (uploads/file['id']).write_bytes(saved);(uploads/file['id']).chmod(0o600)
        record('GC preview/apply/idempotency, stale-part/orphan removal, active/fresh/symlink protection, missing-attachment diagnosis')
        # Real rollback is explicit point-in-time restore, NOT reverse-replaying M4 writes.
        rollback=h.root/'rollback-m3';h.cli('storage','restore','--from',pre_backup,'--data',rollback)
        h.data=rollback;h.admin=rollback/'admin.json';h.binary=original;h.start_server();node_up()
        assert h.cli('message','get',msg['id'],'--config',b)['text']==msg['text']
        assert h.cli('message','get',committed['id'],'--config',b,expect_ok=False)['error']['code']=='not_found'
        wait_for(lambda:h.service_state(svc['id'])=='online');_,port,_=h.mapping(na,'migration-echo');echo_once(port,b'old M3 restored')
        record('published M3 runs pre-upgrade full backup with original identities and network; post-backup writes intentionally absent')
        stop_all()
        return {'status':'PASS','checks':results,'oldBinarySHA256':hashlib.sha256(original.read_bytes()).hexdigest(),'newBinarySHA256':hashlib.sha256(new.read_bytes()).hexdigest(),'rollbackSemantics':'point-in-time restore; later messages and revocations are not carried forward','originalPythonImport':False}
    finally:
        h.close()
        if echo:echo.shutdown();echo.server_close()
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--old-binary',required=True);p.add_argument('--new-binary',default=ROOT/'bin/seven-air-link');a=p.parse_args()
    target=ROOT/'reports/migration-m4.json'
    try:r=run(a.old_binary,a.new_binary)
    except Exception as e:target.write_text(json.dumps({'status':'FAIL','error':str(e)},ensure_ascii=False,indent=2)+'\n');raise
    target.write_text(json.dumps(r,ensure_ascii=False,indent=2)+'\n')
