#!/usr/bin/env python3
"""POSIX terminal regression: passwords do not echo and terminal mode restores."""
import argparse,json,os,pty,select,subprocess,termios,time
from e2e import Harness,ROOT
def run(binary):
    h=Harness(binary);master=slave=None;p=None
    try:
        h.start_server();master,slave=pty.openpty();before=termios.tcgetattr(slave);secret='Pty password NOT echoed 2026'
        p=subprocess.Popen([str(h.binary),'auth','password','set','--config',str(h.admin),'--output','json'],stdin=slave,stdout=subprocess.PIPE,stderr=slave)
        transcript=bytearray()
        def wait(prompt):
            deadline=time.monotonic()+8
            while prompt.encode() not in transcript:
                if time.monotonic()>deadline:raise AssertionError('terminal prompt did not arrive')
                if select.select([master],[],[],.1)[0]:transcript.extend(os.read(master,4096))
        wait('Set administrator web password: ');assert not termios.tcgetattr(slave)[3]&termios.ECHO
        os.write(master,(secret+'\n').encode());wait('Confirm password: ');assert not termios.tcgetattr(slave)[3]&termios.ECHO
        os.write(master,(secret+'\n').encode());out=p.communicate(timeout=10)[0]
        while select.select([master],[],[],.05)[0]:transcript.extend(os.read(master,4096))
        assert p.returncode==0 and json.loads(out)['password']['enabled'];assert secret.encode() not in transcript and secret.encode() not in out
        assert termios.tcgetattr(slave)[3]&termios.ECHO==before[3]&termios.ECHO
        return {'status':'PASS','terminal':'POSIX PTY','checks':['set/confirm disable echo','password never printed','ECHO restored','Server password set successful']}
    finally:
        if p and p.poll() is None:p.kill();p.wait()
        for fd in (master,slave):
            if fd is not None:os.close(fd)
        h.close()
if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--binary',default=ROOT/'bin/seven-air-link');a=p.parse_args();(ROOT/'reports').mkdir(exist_ok=True);r=run(a.binary);(ROOT/'reports/rc3-pty.json').write_text(json.dumps(r,indent=2)+'\n');print(json.dumps(r))
