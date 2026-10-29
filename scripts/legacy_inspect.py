#!/usr/bin/env python3
"""Read-only inventory of the Python shared_data directory. NEVER imports/writes it.
Prints counts, SHA-256 and missing/orphan/path issues. Does not print message text.
Exit 0 = clean inventory, 2 = review required, 1 = cannot inspect safely.
"""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path
import re
import sys

FILE_ID=re.compile(r"^[a-f0-9]{32}$")
MAX_STATE=64*1024*1024

def digest(path:Path)->str:
    h=hashlib.sha256()
    with path.open("rb") as f:
        for b in iter(lambda:f.read(1024*1024),b""):
            h.update(b)
    return h.hexdigest()

def inspect(data:Path)->dict:
    data=data.resolve(strict=True)
    state_path=data/"state.json"
    if state_path.is_symlink() or not state_path.is_file():
        raise ValueError("state.json must be a regular, non-symlink file")
    if state_path.stat().st_size>MAX_STATE:
        raise ValueError("state.json exceeds the 64 MiB inspection safety limit")
    before=digest(state_path)
    state=json.loads(state_path.read_text(encoding="utf-8"))
    if not isinstance(state,dict) or not isinstance(state.get("messages",[]),list) or not isinstance(state.get("files",[]),list):
        raise ValueError("unrecognized Python state format")
    uploads=data/"uploads"
    if uploads.is_symlink():
        raise ValueError("uploads must not be a symlink")
    items=[];issues=[];seen=set()
    for entry in state.get("files",[]):
        if not isinstance(entry,dict):
            issues.append({"issue":"invalid_file_metadata"});continue
        fid=entry.get("id")
        if not isinstance(fid,str) or not FILE_ID.fullmatch(fid):
            issues.append({"issue":"unsafe_or_invalid_file_id"});continue
        if fid in seen:issues.append({"issue":"duplicate_file_index","id":fid})
        seen.add(fid);path=uploads/fid
        if path.is_symlink():
            issues.append({"issue":"symlink_attachment_rejected","id":fid});continue
        if not path.is_file():
            issues.append({"issue":"missing_attachment","id":fid});continue
        size=path.stat().st_size
        if entry.get("size")!=size:issues.append({"issue":"size_mismatch","id":fid,"actualBytes":size})
        items.append({"id":fid,"size":size,"sha256":digest(path)})
    orphans=[]
    if uploads.exists():
        for f in sorted(uploads.iterdir()):
            if f.name not in seen:orphans.append(f.name)
    if orphans:issues.append({"issue":"unindexed_uploads","count":len(orphans)})
    after=digest(state_path)
    if before!=after:raise ValueError("state changed while inspecting; stop the Python writer and retry")
    note=state.get("note",{})
    return {"mode":"read_only_inventory","dataDir":str(data),"stateSHA256":before,"messageCount":len(state.get("messages",[])),"indexedFileCount":len(state.get("files",[])),"verifiedFileCount":len(items),"noteCharacters":len(note.get("text","")) if isinstance(note,dict) and isinstance(note.get("text",""),str) else None,"attachments":items,"unindexedUploads":orphans,"issues":issues,"migrationPerformed":False,"identityPolicy":"Future import must mark records legacy-import; old clientId does not establish a trusted Principal."}

def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument("--data",required=True,type=Path);args=p.parse_args()
    try:
        report=inspect(args.data);print(json.dumps(report,ensure_ascii=False,indent=2));return 2 if report["issues"] else 0
    except (OSError,ValueError,TypeError,UnicodeError) as exc:
        print(json.dumps({"error":"legacy_inspection_failed","message":str(exc)},ensure_ascii=False),file=sys.stderr);return 1
if __name__=="__main__":raise SystemExit(main())
