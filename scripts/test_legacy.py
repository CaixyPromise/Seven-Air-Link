#!/usr/bin/env python3
import json
import tempfile
import unittest
from pathlib import Path
from legacy_inspect import inspect, digest

class LegacyInspectionTests(unittest.TestCase):
    def test_inventory_preserves_bytes_and_detects_missing_and_orphans(self):
        with tempfile.TemporaryDirectory() as tmp:
            d=Path(tmp);u=d/"uploads";u.mkdir();fid="a"*32;missing="b"*32
            (u/fid).write_bytes("测试文件\n".encode());(u/"orphan").write_bytes(b"old")
            raw=json.dumps({"messages":[{"name":"Mac","text":"旧消息"}],"files":[{"id":fid,"size":(u/fid).stat().st_size},{"id":missing,"size":2},{"id":"../../escape"}],"note":{"text":"旧便签"}},ensure_ascii=False).encode()
            (d/"state.json").write_bytes(raw);before={str(x.relative_to(d)):digest(x) for x in d.rglob("*") if x.is_file()}
            r=inspect(d);after={str(x.relative_to(d)):digest(x) for x in d.rglob("*") if x.is_file()}
            self.assertEqual(before,after);self.assertEqual(r["messageCount"],1);self.assertEqual(r["verifiedFileCount"],1);self.assertFalse(r["migrationPerformed"])
            self.assertEqual({x["issue"] for x in r["issues"]},{"missing_attachment","unsafe_or_invalid_file_id","unindexed_uploads"})
    def test_invalid_utf8_is_not_silently_replaced(self):
        with tempfile.TemporaryDirectory() as tmp:
            d=Path(tmp);(d/"state.json").write_bytes(b"\xff")
            with self.assertRaises(UnicodeError):inspect(d)
    def test_symlink_attachment_is_not_followed(self):
        with tempfile.TemporaryDirectory() as tmp:
            d=Path(tmp);(d/"uploads").mkdir();fid="f"*32;(d/"secret").write_text("private");(d/"uploads"/fid).symlink_to(d/"secret")
            (d/"state.json").write_text(json.dumps({"files":[{"id":fid}]}));r=inspect(d)
            self.assertEqual(r["verifiedFileCount"],0);self.assertEqual(r["issues"][0]["issue"],"symlink_attachment_rejected")

if __name__=="__main__":unittest.main()
