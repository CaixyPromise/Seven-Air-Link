#!/usr/bin/env python3
"""Actual rc.3 binary -> rc.4, backup/restore and rollback, on disposable data.
The old Owner-password behavior is reproduced solely to verify its removal.
No private state or credentials are retained in the report.
"""
import argparse
import hashlib
import json
import signal
import sqlite3
from pathlib import Path
from e2e import Harness, ROOT


def run(old_binary,new_binary):
    old=Path(old_binary).resolve();new=Path(new_binary).resolve()
    h=Harness(old);checks=[]
    old_password='Old shared administrator password 2026'
    new_password='New independent joining password 2026'
    def passed(test):checks.append(test);print('PASS',test,flush=True)
    try:
        assert h.cli('version')['version']=='0.1.0-rc.3'
        h.start_server()
        h.cli('auth','password','set','--password-stdin','--config',h.admin,stdin=old_password+'\n')
        original_note='rc.3 便签\n  原始正文和缩进'
        h.cli('note','set','--revision','0','--text',original_note,'--config',h.admin)
        msg=h.cli('message','send','--to','shared','--text','Existing shared message','--config',h.admin)['message']
        node,node_id=h.pair('Existing Node','node')
        bot=h.root/'existing-bot.json';h.cli('bot','bind','--name','Existing Bot','--out',bot,'--config',h.admin)
        bot_id=h.cli('identity','whoami','--config',bot)['id']
        owner_token=h.owner['token'];owner_id=h.owner['subjectId'];fingerprint=h.owner['fingerprint']
        h.server.terminate();h.server.wait(timeout=5)
        baseline=h.root/'pre-upgrade'
        backup=h.cli('storage','backup','--data',h.data,'--out',baseline)
        assert backup['schema']==5,backup
        passed('real rc.3 initializes schema 5, Owner password, identities, messages and note')
        h.binary=new;h.start_server()
        assert h.owner['token']==owner_token and h.owner['fingerprint']==fingerprint
        assert h.cli('auth','password','status','--config',h.admin)['enabled'] is False
        status,body=h.request('POST','/api/v1/sessions/password',{'password':old_password,'name':'old password'},auth=False)
        assert status==403,(status,body)
        passed('schema upgrade disables old administrator password; keeps Owner token/certificate')
        note=h.cli('note','get','--config',bot)
        assert note['text']==original_note and note['revision']==1 and note['updatedBy']==owner_id
        assert h.cli('identity','whoami','--config',bot)['id']==bot_id
        assert h.cli('message','get',msg['id'],'--config',bot)['text']==msg['text']
        assert any(d['id']==node_id for d in h.cli('devices','list','--config',h.admin)['devices'])
        passed('existing Bot/Node identities, message ID and note revision survive migration')
        h.cli('auth','password','set','--password-stdin','--config',h.admin,stdin=new_password+'\n')
        a=h.root/'new-client.json'
        client=h.cli('client','login',h.url,'--fingerprint',fingerprint,'--name','New CLI Member','--password-stdin','--config',a,stdin=new_password+'\n')
        assert client['principal']['role']=='member' and client['principal']['id']!=owner_id
        h.cli('note','set','--revision','1','--text','schema 6 attributed update','--config',a)
        updated=h.cli('note','get','--config',a)
        assert updated['source']['clientId']==client['client']['id']
        passed('new joining password creates Member; new note stores exact credential provenance')
        h.server.kill();h.server.wait(timeout=5);h.start_server()
        assert h.cli('note','get','--config',a)==updated
        passed('crash recovery retains independent client credential and complete note state')
        h.server.terminate();h.server.wait(timeout=5)
        backup6=h.root/'after-upgrade';h.cli('storage','backup','--data',h.data,'--out',backup6)
        verified=h.cli('storage','verify','--from',backup6);assert verified['ok'],verified
        restored=h.root/'restored6';h.cli('storage','restore','--from',backup6,'--data',restored)
        h.data=restored;h.admin=restored/'admin.json';h.start_server()
        assert h.cli('note','get','--config',a)==updated
        assert h.cli('identity','whoami','--config',a)['id']==client['principal']['id']
        passed('schema 6 backup restores to a new directory and actually serves same identities/note')
        h.server.terminate();h.server.wait(timeout=5)
        old_restored=h.root/'restored5';h.cli('storage','restore','--from',baseline,'--data',old_restored)
        h.data=old_restored;h.admin=old_restored/'admin.json';h.binary=old;h.start_server()
        assert h.cli('note','get','--config',h.admin)['text']==original_note
        assert h.cli('auth','password','status','--config',h.admin)['enabled']
        assert h.cli('identity','whoami','--config',bot)['id']==bot_id
        passed('rollback uses pre-upgrade full backup and real old binary, not a downgraded schema-6 file')
        return {'status':'PASS','count':len(checks),'checks':checks,'oldVersion':'0.1.0-rc.3','newVersion':'0.1.0-rc.4','oldBinarySHA256':hashlib.sha256(old.read_bytes()).hexdigest(),'newBinarySHA256':hashlib.sha256(new.read_bytes()).hexdigest(),'physicalMacWindows':False}
    finally:h.close()

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--old-binary',required=True);p.add_argument('--new-binary',required=True);a=p.parse_args()
    out=ROOT/'reports/identity-migration.json';out.parent.mkdir(exist_ok=True)
    try:r=run(a.old_binary,a.new_binary)
    except Exception as e:
        out.write_text(json.dumps({'status':'FAIL','error':str(e)},indent=2)+'\n');raise
    out.write_text(json.dumps(r,ensure_ascii=False,indent=2)+'\n');print(json.dumps(r,ensure_ascii=False))
