#!/usr/bin/env python3
"""Optional independent RFC6455 control interoperability test (websockets >= 15).
The application has NO Python/WebSocket-package runtime dependency.
"""
from __future__ import annotations
import argparse, json, ssl, socket, threading
from pathlib import Path
from e2e import Harness, ROOT, wait_for
import websockets
from websockets.sync.client import connect
from websockets.sync.server import serve

def run(binary):
    h=Harness(binary, heartbeat="100ms", offline="1s")
    checks=[]
    try:
        h.start_server()
        config,did=h.pair("Independent Python peer", "interop")
        cfg=json.loads(config.read_text())
        tls=ssl.create_default_context(cafile=str(h.data/"server.crt"))
        with connect(h.url.replace("https:","wss:")+"/api/v1/node/control",ssl=tls,subprotocols=["slb.control.v1"],additional_headers={"Authorization":"Bearer "+cfg["token"],"X-Bridge-Protocol":"1"},compression=None,proxy=None) as ws:
            welcome=json.loads(ws.recv(timeout=3));assert welcome["type"]=="welcome" and welcome["protocol"]==1
            for seq in range(1,4):
                payload=json.dumps({"type":"heartbeat","seq":seq})
                ws.send([payload[:10],payload[10:]])
                ack=json.loads(ws.recv(timeout=3));assert ack["type"]=="heartbeat.ack" and ack["seq"]==seq
            assert ws.ping(b"rfc6455").wait(2)
        wait_for(lambda:h.states()[did]=="offline",detail="independent client disconnect")
        checks.append("Python websockets client -> Go Server: TLS, upgrade, masked fragmentation, heartbeat, ping/pong, close")
        stls=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        stls.load_cert_chain(h.data/"server.crt",h.data/"server.key")
        observed=threading.Event(); errors=[]
        def handler(ws):
            try:
                assert ws.request.headers["Authorization"]=="Bearer "+cfg["token"]
                assert ws.request.path=="/api/v1/node/control" and ws.subprotocol=="slb.control.v1"
                ws.send(json.dumps({"type":"welcome","protocol":1,"sessionId":"independent","heartbeatMs":100,"timeoutMs":1000}))
                for seq in range(1,4):
                    heartbeat=json.loads(ws.recv(timeout=3));assert heartbeat["type"]=="heartbeat" and heartbeat["seq"]==seq
                    body=json.dumps({"type":"heartbeat.ack","seq":seq})
                    ws.send([body[:8],body[8:]])
                assert ws.ping().wait(2)
                observed.set()
                try:
                    while True:
                        msg=json.loads(ws.recv(timeout=3));ws.send(json.dumps({"type":"heartbeat.ack","seq":msg["seq"]}))
                except websockets.exceptions.ConnectionClosed:
                    pass
            except Exception as exc:
                errors.append(str(exc));observed.set()
        with serve(handler,"127.0.0.1",0,ssl=stls,subprotocols=["slb.control.v1"],compression=None) as srv:
            port=srv.socket.getsockname()[1]
            thread=threading.Thread(target=srv.serve_forever);thread.start()
            node=h.launch(["node","start","--config",config,"--server",f"https://localhost:{port}"],"python-server-node")
            try:
                assert observed.wait(6),"Go Node did not complete independent handshake/heartbeats"
                assert not errors,errors
                node.terminate();node.wait(timeout=4)
            finally:
                srv.shutdown();thread.join(timeout=4)
        assert not errors,errors
        checks.append("Go Node -> Python websockets server: pinned TLS, authenticated upgrade, masked client frames, fragmented ACK, ping/pong")
        return {"status":"PASS","independentLibrary":"Python websockets "+websockets.__version__,"checks":checks,"scope":"control channel only; no TCP data-plane validation"}
    finally:h.close()
if __name__=="__main__":
    p=argparse.ArgumentParser();p.add_argument("--binary",default=str(ROOT/"bin/seven-air-link"));a=p.parse_args()
    result=run(a.binary);(ROOT/"reports/interop.json").write_text(json.dumps(result,ensure_ascii=False,indent=2)+"\n");print(json.dumps(result,ensure_ascii=False,indent=2))
