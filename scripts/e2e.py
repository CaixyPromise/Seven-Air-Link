#!/usr/bin/env python3
"""Black-box M1 acceptance using real Go processes and TLS sockets.
No Python third-party dependencies. SIGSTOP fault injection is Linux/macOS only.
Run from repo root: python3 scripts/e2e.py --binary bin/seven-air-link
"""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import signal
import socket
import ssl
import subprocess
import tempfile
import sqlite3
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]

def wait_for(check, timeout=10.0, detail="condition"):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            value = check()
            if value:
                return value
        except (OSError, RuntimeError, ValueError) as exc:
            last = exc
        time.sleep(0.08)
    raise AssertionError(f"Timed out waiting for {detail}; last error: {last}")

class Harness:
    def __init__(self, binary, heartbeat="5s", offline="15s"):
        self.binary = Path(binary).resolve()
        self.tmp = tempfile.TemporaryDirectory(prefix="slb-e2e-")
        self.root = Path(self.tmp.name)
        self.data = self.root / "server"
        self.admin = self.data / "admin.json"
        self.heartbeat, self.offline = heartbeat, offline
        self.processes, self.handles = [], []
        self.counter = 0
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            self.port = sock.getsockname()[1]
        self.url = f"https://localhost:{self.port}"
        self.server = None

    def launch(self, args, label):
        self.counter += 1
        out = open(self.root / f"{self.counter}-{label}.stdout", "w", encoding="utf-8")
        err = open(self.root / f"{self.counter}-{label}.stderr", "w", encoding="utf-8")
        self.handles.extend((out, err))
        p = subprocess.Popen([str(self.binary), *map(str, args)], stdout=out, stderr=err)
        self.processes.append(p)
        return p

    def cli(self, *args, expect_ok=True, stdin=None, timeout=12):
        p = subprocess.run([str(self.binary), *map(str,args), "--output", "json"], input=stdin, text=True, capture_output=True, timeout=timeout)
        if expect_ok and p.returncode:
            # Arguments never contain raw credentials. Do not print successful secrets.
            raise AssertionError(f"CLI failed ({p.returncode}): {args!r}\n{p.stdout}\n{p.stderr}")
        if not expect_ok:
            assert p.returncode != 0, f"Command unexpectedly succeeded: {args!r}"
        try:
            return json.loads(p.stdout)
        except json.JSONDecodeError as exc:
            raise AssertionError(f"CLI did not return JSON: {p.stdout!r}; stderr={p.stderr!r}") from exc

    def start_server(self):
        self.server = self.launch(["serve", "--data", self.data, "--listen", f"127.0.0.1:{self.port}", "--heartbeat", self.heartbeat, "--offline-after", self.offline], "server")
        def ready():
            if self.server.poll() is not None:
                raise RuntimeError("server exited before readiness")
            if not self.admin.exists():
                return False
            try:
                return self.request("GET", "/readyz", auth=False)[0] == 200
            except (OSError, ssl.SSLError):
                return False
        wait_for(ready, detail="server readiness")
        self.owner = json.loads(self.admin.read_text())
        return self.server

    def request(self, method, path, body=None, auth=True, token=None, headers=None):
        context = ssl.create_default_context(cafile=str(self.data / "server.crt"))
        h = dict(headers or {})
        if body is not None:
            h["Content-Type"] = "application/json"
        if auth:
            cfg = json.loads(self.admin.read_text())
            h["Authorization"] = "Bearer " + (token or cfg["token"])
        req = urllib.request.Request(self.url+path, data=None if body is None else json.dumps(body).encode(), headers=h, method=method)
        try:
            response = urllib.request.urlopen(req, context=context, timeout=5)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw=response.read()
            return response.status, json.loads(raw) if raw else {}

    def pair(self, name, profile, code=None):
        code_path = self.root / f"{profile}.code"
        if code is None:
            self.cli("pairing", "create", "--config", self.admin, "--out", code_path)
        else:
            code_path.write_text(code)
            code_path.chmod(0o600)
        cfg = self.root / f"{profile}.json"
        result = self.cli("join", self.url, "--fingerprint", self.owner["fingerprint"], "--name", name, "--code-file", code_path, "--config", cfg)
        return cfg, result["deviceId"]

    def node(self, profile):
        return self.launch(["node", "start", "--config", profile], "node")

    def status(self):
        status, data = self.request("GET", "/api/v1/status")
        assert status == 200
        return data

    def states(self):
        return {d["id"]: d["status"] for d in self.status()["devices"]}

    def close(self):
        # Resume stopped processes before terminating them; otherwise SIGTERM queues.
        for p in reversed(self.processes):
            if p.poll() is None:
                if hasattr(signal,"SIGCONT"):
                    try: p.send_signal(signal.SIGCONT)
                    except ProcessLookupError: pass
                p.terminate()
        for p in self.processes:
            try: p.wait(timeout=6)
            except subprocess.TimeoutExpired:
                p.kill();p.wait(timeout=3)
        for f in self.handles:
            f.close()
        self.tmp.cleanup()


def run_suite(binary):
    h=Harness(binary)
    results=[]
    def record(name, **detail):
        results.append({"test":name,"result":"PASS",**detail})
        print("PASS",name, json.dumps(detail,ensure_ascii=False) if detail else "",flush=True)
    try:
        h.start_server()
        owner_before=hashlib.sha256(h.admin.read_bytes()).hexdigest()
        status,data=h.request("GET","/healthz",auth=False)
        assert status==200 and data["ok"]
        record("TLS server startup and readiness")
        cfg_a,id_a=h.pair("Dev-A · Linux", "node-a")
        cfg_b,id_b=h.pair("Dev-B · Linux", "node-b")
        a,b=h.node(cfg_a),h.node(cfg_b)
        wait_for(lambda:h.status()["onlineDevices"]==2,detail="two live Nodes")
        record("two independently paired real Node processes")
        assert h.cli("doctor","--config",h.admin)["checks"][0]["result"]=="pass"
        record("CLI status and doctor")
        error=h.cli("serve","--data",h.data,"--listen",f"127.0.0.1:{h.port}",expect_ok=False)
        assert "already in use" in error["error"]["message"]
        error=h.cli("node","start","--config",cfg_a,expect_ok=False)
        assert "already in use" in error["error"]["message"]
        record("cross-process Server/Node profile locks")
        replay=h.cli("join",h.url,"--name","replay","--fingerprint",h.owner["fingerprint"],"--code-file",h.root/"node-a.code","--config",h.root/"replay.json",expect_ok=False)
        assert replay["error"]["code"]=="pairing_invalid_or_expired"
        record("consumed invitation cannot be replayed")
        denied=h.cli("pairing","create","--config",cfg_a,expect_ok=False)
        assert denied["error"]["code"]=="access_denied"
        devices=h.cli("devices","list","--config",cfg_a)["devices"]
        assert len(devices)==1 and devices[0]["id"]==id_a
        record("device credential has no owner rights or cross-device visibility")
        h.cli("pairing","create","--config",h.admin,"--out",h.root/"bad-pin.code")
        bad=h.cli("join",h.url,"--name","wrong pin","--fingerprint","0"*64,"--code-file",h.root/"bad-pin.code","--config",h.root/"bad-pin.json",expect_ok=False)
        assert "fingerprint mismatch" in bad["error"]["message"] and not (h.root/"bad-pin.json").exists()
        assert len(h.status()["devices"])==2
        record("wrong TLS fingerprint rejected before device enrollment")
        # Default production timing, not just accelerated unit-test timing.
        if not hasattr(signal,"SIGSTOP"):
            raise RuntimeError("This fault suite requires SIGSTOP/SIGCONT; run it on Linux/macOS")
        paused=time.monotonic();b.send_signal(signal.SIGSTOP)
        wait_for(lambda:h.states()[id_b]=="offline",timeout=20,detail="silent Node marked offline")
        offline_seconds=time.monotonic()-paused
        assert h.states()[id_a]=="online" and offline_seconds<=20
        b.send_signal(signal.SIGCONT)
        wait_for(lambda:h.states()[id_b]=="online",timeout=12,detail="Node resumes and reconnects")
        record("silent Node deadline and resume recovery at default 5s/15s timing",offlineDetectionSeconds=round(offline_seconds,3))
        # SIGKILL tests durable state and lock release, not only graceful shutdown.
        h.server.kill();h.server.wait(timeout=3)
        time.sleep(.35)
        assert a.poll() is None and b.poll() is None
        with sqlite3.connect(h.data/"bridge.db") as dbconn:
            persisted={"events":[json.loads(row[0]) for row in dbconn.execute("SELECT data FROM events ORDER BY seq")],"devices":{row[0]:json.loads(row[1]) for row in dbconn.execute("SELECT id,data FROM devices")}}
        assert all(v["device"]["status"]!="online" for v in persisted["devices"].values())
        assert h.cli("status","--config",h.admin,expect_ok=False)["error"]["code"]=="connection_failed"
        started=time.monotonic();h.start_server()
        wait_for(lambda:h.status()["onlineDevices"]==2,timeout=12,detail="Nodes reconnect after Server SIGKILL")
        assert hashlib.sha256(h.admin.read_bytes()).hexdigest()==owner_before
        assert set(h.states())=={id_a,id_b}
        record("Server SIGKILL/restart retains authority, releases lock and reconnects Nodes",reconnectSeconds=round(time.monotonic()-started,3))
        # Persisted audit contains no raw owner/device tokens or invitation codes.
        audit=json.dumps(h.request("GET","/api/v1/activity")[1])
        for cfg in (h.admin,cfg_a,cfg_b):
            assert json.loads(cfg.read_text())["token"] not in audit
        record("activity excludes credential secrets")
        watch_out=open(h.root/"watch.ndjson","w",encoding="utf-8")
        watch_err=open(h.root/"watch.err","w",encoding="utf-8")
        h.handles.extend((watch_out,watch_err))
        cursor=h.cli("activity","list","--config",h.admin)["nextCursor"]
        watch=subprocess.Popen([str(h.binary),"activity","watch","--config",str(h.admin),"--cursor",str(cursor),"--cursor-file",str(h.root/"cursor"),"--output","ndjson"],stdout=watch_out,stderr=watch_err)
        h.processes.append(watch);time.sleep(.2)
        h.cli("devices","rename",id_b,"--name","Dev-B renamed","--config",h.admin)
        wait_for(lambda:(h.root/"watch.ndjson").read_text().strip(),detail="SSE CLI watch delivers rename")
        events=[json.loads(x) for x in (h.root/"watch.ndjson").read_text().splitlines()]
        assert any(x["action"]=="device.renamed" for x in events)
        wait_for(lambda:(h.root/"cursor").exists(),detail="event cursor commit")
        assert int((h.root/"cursor").read_text())>cursor
        watch.terminate();watch.wait(timeout=4)
        record("CLI SSE watch and explicit cursor persistence")
        revoked=time.monotonic();h.cli("devices","revoke",id_a,"--config",h.admin)
        wait_for(lambda:a.poll() is not None,timeout=3,detail="revoked Node exits")
        assert a.returncode!=0 and h.states()[id_a]=="revoked" and h.states()[id_b]=="online"
        again=h.cli("node","start","--config",cfg_a,expect_ok=False)
        assert again["error"]["retryable"] is False
        record("revocation closes active Node and blocks rejoin",revocationSeconds=round(time.monotonic()-revoked,3))
        b.terminate();b.wait(timeout=4)
        wait_for(lambda:h.states()[id_b]=="offline",detail="graceful Node shutdown")
        h.server.terminate();h.server.wait(timeout=5)
        assert h.server.returncode==0
        record("graceful Node and Server shutdown")
        return {"status":"PASS","scope":"one Linux host, independent OS processes, real TLS/WSS sockets; not physical Mac/Windows interoperability","timing":{"heartbeat":"5s","offlineAfter":"15s"},"tests":results}
    finally:
        h.close()

if __name__=="__main__":
    parser=argparse.ArgumentParser()
    parser.add_argument("--binary",default=str(ROOT/"bin/seven-air-link"))
    parser.add_argument("--report",default=str(ROOT/"reports/e2e.json"))
    args=parser.parse_args()
    report=run_suite(args.binary)
    dest=Path(args.report);dest.parent.mkdir(parents=True,exist_ok=True)
    dest.write_text(json.dumps(report,ensure_ascii=False,indent=2)+"\n")
