#!/usr/bin/env python3
"""M3 real-process acceptance. Two Nodes and independent Human/Bot CLI profiles.
The tests do not invoke a model and do not claim native macOS/Windows coverage.
"""
from __future__ import annotations
import sqlite3
import argparse, hashlib, json, os, signal, socket, subprocess, time
from pathlib import Path
from e2e import wait_for
from e2e_m2 import NetworkHarness, ThreadedTCP, Echo, spawn_server, recv_exact, free_port
ROOT=Path(__file__).resolve().parents[1]

def suite(binary):
    h=NetworkHarness(binary); checks=[]; servers=[]
    def record(name,**evidence):
        checks.append({'test':name,'result':'PASS',**evidence}); print('PASS',name,evidence,flush=True)
    def run(*args,stdin=None,expect_ok=True,timeout=12):
        p=subprocess.run([str(h.binary),*map(str,args)],input=stdin,text=True,capture_output=True,timeout=timeout)
        if expect_ok: assert p.returncode==0,(p.returncode,p.stdout,p.stderr)
        else: assert p.returncode!=0,'unexpected success'
        return p
    def create(name,kind='bot',device=None):
        cfg=h.root/(name.lower()+'.json');args=['bot','bind'] if kind=='bot' else ['identity','create','--type','human']
        args+=['--name',name,'--out',cfg,'--config',h.admin]
        if device:args+=['--device',device]
        result=h.cli(*args);return cfg,result['principal']['id']
    def stream(cfg,label,*extra):
        p=h.launch(['message','watch','--config',cfg,'--output','ndjson',*extra],label)
        return p,h.root/f'{h.counter}-{label}.stdout'
    def listening(id):
        peers=h.cli('peers','list','--config',h.admin)['peers'];return next(p['listening'] for p in peers if p['id']==id)
    def incoming(cfg,cursor,to):
        return run('message','watch','--incoming-only','--with',to,'--cursor-file',cursor,'--timeout','3s','--max-messages','1','--config',cfg,'--output','ndjson')
    try:
        h.start_server();na,da=h.pair('Source Node','source');nb,db=h.pair('Target Node','target');h.node(na);h.node(nb)
        wait_for(lambda:h.status()['onlineDevices']==2)
        a,aid=create('MacBot',device=da);b,bid=create('WindowsBot',device=db);human,hid=create('Developer','human');other,oid=create('Observer')
        for cfg in [a,b,human,other]:
            data=json.loads(cfg.read_text());assert data['role']=='member';assert data['token']!=h.owner['token'];assert cfg.stat().st_mode&0o077==0
        assert h.cli('identity','whoami','--config',a)['id']==aid
        peers=h.cli('peers','list','--config',a)['peers'];assert {p['type'] for p in peers}=={'human','bot'}
        record('two real Nodes; Human/Bot independent private profiles and shared discovery')
        # None of these profiles has Owner or Device authority.
        assert h.cli('bot','bind','--name','escape','--out',h.root/'escape.json','--config',a,expect_ok=False)['error']['code']=='access_denied'
        assert h.cli('devices','revoke',db,'--config',a,expect_ok=False)['error']['code']=='access_denied'
        assert h.cli('message','send','--text','device is not a principal','--config',na,expect_ok=False)['error']['code']=='access_denied'
        record('member cannot mint identities or revoke devices; Device cannot impersonate a messenger')
        text='验证接口 /health\n中文、缩进与 "引号" 保留。\nPRIVATE-NOT-FOR-OWNER'
        sent=h.cli('message','send','--to',bid,'--stdin','--idempotency-key','work-001','--correlation-id','review-001','--config',a,stdin=text)
        msg=sent['message'];assert msg['senderId']==aid and msg['status']=='accepted' and msg['text']==text
        assert h.cli('message','get',msg['id'],'--config',b)['text']==text
        reply=h.cli('message','send','--to',aid,'--text','收到请求（不代表执行完成）','--reply-to',msg['id'],'--correlation-id','review-001','--config',b)['message']
        for cfg in [human,other,h.admin]:assert h.cli('message','get',msg['id'],'--config',cfg,expect_ok=False)['error']['code']=='not_found'
        record('UTF-8 Bot-to-Bot direct message/reply; third-party Human and Owner API privacy')
        dup=h.cli('message','send','--to',bid,'--stdin','--idempotency-key','work-001','--correlation-id','review-001','--config',a,stdin=text)
        assert dup['duplicate'] and dup['message']['id']==msg['id']
        assert h.cli('message','send','--to',bid,'--text','different','--idempotency-key','work-001','--config',a,expect_ok=False)['error']['code']=='idempotency_conflict'
        tricky=h.cli('message','send','--to',bid,'--text','--config','--config',a)['message'];assert tricky['text']=='--config'
        h.cli('message','send','--stdin','--config',a,stdin='x'*32769,expect_ok=False)
        record('send idempotency and conflict detection; safe CLI flag-value parsing and size rejection')
        shared=h.cli('message','send','--to','shared','--text','共享频道 · human/bot','--config',human)['message']
        assert shared['id'] in [m['id'] for m in h.cli('message','list','--with','shared','--config',b)['messages']]
        first=h.cli('message','list','--with',aid,'--limit','1','--config',b);assert first['hasMore']
        older=h.cli('message','list','--with',aid,'--before',first['nextCursor'],'--limit','1','--config',b);assert older['messages'][0]['seq']<first['messages'][0]['seq']
        assert listening(bid)==0
        offline=h.cli('message','send','--to',bid,'--text','尚未订阅时也会保存','--config',a)['message']
        assert offline['status']=='accepted'
        record('shared Human/Bot workspace; bounded historical pagination; offline acceptance without execution claims')
        # Scope-bound replay file. Skip self-authored messages, but advance past them.
        cursor=h.root/'windows.cursor';p=incoming(b,cursor,aid);one=json.loads(p.stdout);assert one['id']==msg['id']
        p=incoming(b,cursor,aid);two=json.loads(p.stdout);assert two['id']==tricky['id'] and two['id']!=reply['id']
        p=incoming(b,cursor,aid);assert json.loads(p.stdout)['id']==offline['id']
        assert json.loads(cursor.read_text())['subjectId']==bid
        run('message','watch','--incoming-only','--with',aid,'--cursor-file',cursor,'--timeout','100ms','--config',a,'--output','json',expect_ok=False)
        p=run('message','watch','--incoming-only','--with',aid,'--cursor-file',cursor,'--timeout','300ms','--config',b,'--output','ndjson');assert p.stdout.strip()==''
        record('CLI NDJSON replay/resume; incoming-only skips own replies; cursor rejects identity mismatch')
        # Genuine live long-poll stream before send, not only replay.
        watcher,out=stream(b,'live-watch','--incoming-only','--with',aid,'--cursor-file',cursor,'--timeout','5s','--max-messages','1')
        wait_for(lambda:listening(bid)>0,detail='live inbox subscription')
        start=time.monotonic();live=h.cli('message','send','--to',bid,'--text','实时发送','--config',a)['message'];watcher.wait(timeout=6)
        assert watcher.returncode==0 and json.loads(out.read_text())['id']==live['id']
        record('live CLI message delivery through real HTTPS SSE',elapsedSeconds=round(time.monotonic()-start,3))
        bad=h.root/'unreachable.json';cfg=json.loads(b.read_text());cfg['server']=f'https://localhost:{free_port()}';bad.write_text(json.dumps(cfg));bad.chmod(0o600)
        failure=run('message','watch','--timeout','200ms','--config',bad,'--output','json',expect_ok=False);assert json.loads(failure.stdout)['error']['code']=='inbox_unavailable'
        record('unreachable inbox reports nonzero error instead of false empty success')
        payload=os.urandom(1024*1024)+'跨机器附件'.encode();source=h.root/'测试附件.bin';source.write_bytes(payload)
        attachment=h.cli('file','send',source,'--to',bid,'--config',a);assert attachment['sha256']==hashlib.sha256(payload).hexdigest()
        h.cli('message','send','--to',bid,'--text','附件已保存','--attach',attachment['id'],'--config',a)
        received=h.root/'received.bin';h.cli('file','get',attachment['id'],'--out',received,'--config',b);assert received.read_bytes()==payload
        h.cli('file','get',attachment['id'],'--out',received,'--config',b,expect_ok=False)
        assert h.cli('file','get',attachment['id'],'--out',h.root/'stolen.bin','--config',other,expect_ok=False)['error']['code']=='not_found'
        assert h.cli('message','send','--to','shared','--text','leak','--attach',attachment['id'],'--config',b,expect_ok=False)['error']['code']=='access_denied'
        record('1 MiB+ binary CLI attachment round trip with SHA-256; no overwrite or private ACL widening')
        # Owner audit is inspectable metadata, never the contents of private messages.
        activity=h.cli('activity','list','--actor',aid,'--correlation','review-001','--config',h.admin)['events'];assert any(e['resourceId']==msg['id'] for e in activity)
        audit=h.cli('activity','list','--limit','500','--config',h.admin);raw=json.dumps(audit,ensure_ascii=False)
        assert text not in raw and 'PRIVATE-NOT-FOR-OWNER' not in raw
        for profile in [a,b,human,other]:assert json.loads(profile.read_text())['token'] not in raw
        own=h.cli('activity','list','--limit','500','--config',a)['events'];assert all(e['actorId']==aid for e in own)
        record('correlation/actor audit filters; member scope; no private bodies or tokens in activity')
        # Kill/restart with the same disk state, verify both network and collaboration.
        h.server.kill();h.server.wait(timeout=5);h.start_server();wait_for(lambda:h.status()['onlineDevices']==2)
        assert h.cli('message','get',msg['id'],'--config',b)['text']==text
        h.cli('file','get',attachment['id'],'--out',h.root/'after-restart.bin','--config',b)
        assert (h.root/'after-restart.bin').read_bytes()==payload
        assert sqlite3.connect(h.data/'bridge.db').execute('PRAGMA user_version').fetchone()[0]==4
        record('SIGKILL/restart preserves identity, private history, file bytes and Node authorization')
        # Principal grants use the existing M2 protocol, not a special Agent path.
        echo=spawn_server(ThreadedTCP(('127.0.0.1',0),Echo));servers.append(echo)
        service=h.cli('share','tcp','--name','bot-echo','--target',f'127.0.0.1:{echo.server_address[1]}','--config',nb)
        wait_for(lambda:h.service_state(service['id'])=='online')
        assert h.cli('proxy','open','bot-echo','--listen','127.0.0.1:0','--config',b,expect_ok=False)['error']['code']=='access_denied'
        h.cli('services','grants','bot-echo','--allow-principals',aid,'--config',h.admin)
        mapping,port,lease=h.mapping(a,'bot-echo');conn=socket.create_connection(('127.0.0.1',port),timeout=5);conn.sendall(b'bot-access');assert recv_exact(conn,10)==b'bot-access'
        record('device binding grants no implicit service use; explicit Principal ACL opens real TCP tunnel')
        replacement=h.root/'macbot-rotated.json';h.cli('identity','rotate',aid,'--out',replacement,'--config',h.admin)
        conn.settimeout(3)
        try:assert conn.recv(1)==b''
        except (ConnectionResetError,ConnectionAbortedError):pass
        finally:conn.close()
        wait_for(lambda:mapping.poll() is not None,timeout=6,detail='old mapping exits after credential rotation')
        assert h.cli('identity','whoami','--config',a,expect_ok=False)['error']['code']=='unauthenticated'
        assert h.cli('identity','whoami','--config',replacement)['id']==aid
        mapping,port,lease=h.mapping(replacement,'bot-echo');conn=socket.create_connection(('127.0.0.1',port),timeout=5);conn.sendall(b'new');assert recv_exact(conn,3)==b'new'
        record('credential rotation invalidates old profile/lease/flow; new credential preserves Principal ID')
        h.cli('bot','revoke',aid,'--config',h.admin);conn.settimeout(3)
        try:assert conn.recv(1)==b''
        except (ConnectionResetError,ConnectionAbortedError):pass
        finally:conn.close()
        wait_for(lambda:mapping.poll() is not None,timeout=6)
        assert h.cli('identity','whoami','--config',replacement,expect_ok=False)['error']['code']=='principal_inactive'
        assert h.status()['onlineDevices']==2
        record('Bot revocation terminates existing network flows without revoking unrelated Nodes')
        current=h.cli('message','list','--config',b)['snapshotSeq'];watcher,out=stream(b,'revoke-watch','--cursor',current,'--timeout','8s')
        wait_for(lambda:listening(bid)>0)
        start=time.monotonic();h.cli('devices','revoke',db,'--config',h.admin);watcher.wait(timeout=5)
        assert watcher.returncode!=0 and 'stream_invalidated' in out.read_text()
        assert h.cli('identity','whoami','--config',b,expect_ok=False)['error']['code']=='principal_inactive'
        record('linked device revocation disables Bot credentials and closes inbox subscription',elapsedSeconds=round(time.monotonic()-start,3))
        h.cli('file','send',source,'--to','shared','--config',human)
        skill=h.cli('skill','show')['content'];assert '0.1.0-rc.1' in skill and '--incoming-only' in skill and 'Bridge never detects models' in skill
        record('embedded Skill matches working commands; no model runtime or Agent-specific protocol needed')
        return checks
    finally:
        h.close()
        for server in servers:server.shutdown();server.server_close()

if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--binary',default=ROOT/'bin/seven-air-link');args=parser.parse_args()
    out=ROOT/'reports/m3-e2e.json';out.parent.mkdir(exist_ok=True)
    try:
        checks=suite(args.binary)
        out.write_text(json.dumps({'status':'PASS','environment':'Linux amd64, real separate processes and loopback TLS sockets; no LLM execution','checks':checks},ensure_ascii=False,indent=2)+'\n')
    except Exception as exc:
        out.write_text(json.dumps({'status':'FAIL','error':str(exc)},ensure_ascii=False,indent=2)+'\n');raise
