#!/usr/bin/env python3
"""Real loopback-only services used inside each isolated network namespace."""
import argparse, http.server, json, threading, signal
from e2e_m2 import ThreadedTCP, Echo, Digest
p=argparse.ArgumentParser();p.add_argument('--name',required=True);a=p.parse_args()
class HTTP(http.server.BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def do_GET(self):
        data=json.dumps({'environment':a.name,'path':self.path},ensure_ascii=False).encode()
        self.send_response(200);self.send_header('Content-Type','application/json; charset=utf-8');self.send_header('Content-Length',str(len(data)));self.end_headers();self.wfile.write(data)
servers=[http.server.ThreadingHTTPServer(('127.0.0.1',8080),HTTP),ThreadedTCP(('127.0.0.1',8090),Echo),ThreadedTCP(('127.0.0.1',8091),Digest)]
for s in servers:threading.Thread(target=s.serve_forever,daemon=True).start()
print(json.dumps({'ready':True,'environment':a.name,'ports':[8080,8090,8091]}),flush=True)
signal.pause()
