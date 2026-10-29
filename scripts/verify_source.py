#!/usr/bin/env python3
"""Extract a generated source ZIP, check its manifest, rebuild and test offline.

This is a development verification helper, not an application dependency. Only
use it with source archives you trust: go test executes code from that archive.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import os
import platform
import subprocess
import tempfile
import zipfile
from pathlib import Path

def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()

def verify(archive: Path, expected: Path, reports: Path | None) -> dict:
    env = dict(os.environ, GOTOOLCHAIN='local', GOPROXY='off', GOSUMDB='off', CGO_ENABLED='1')
    with tempfile.TemporaryDirectory(prefix='slb-source-verify-') as temp:
        root = Path(temp)
        with zipfile.ZipFile(archive) as z:
            for name in z.namelist():
                target = (root/name).resolve()
                if root not in target.parents:
                    raise ValueError('Unsafe archive path')
            z.extractall(root)
        source = root/'seven-air-link'
        manifest = source/'SOURCE-SHA256SUMS'
        verified = 0
        for line in manifest.read_text().splitlines():
            checksum, name = line.split('  ', 1)
            target = (source/name).resolve()
            if source not in target.parents or digest(target) != checksum:
                raise ValueError(f'Manifest mismatch: {name}')
            verified += 1
        binary = root/'verified-seven-air-link'
        commands = [(['go','build','-buildvcs=false','-trimpath','-ldflags=-s -w','-o',str(binary),'./cmd/seven-air-link'], 'source-build.log'),
                    (['go','test','-count=1','./...'], 'source-tests.log')]
        for command, logname in commands:
            run = subprocess.run(command, cwd=source, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, timeout=180)
            if reports:
                reports.mkdir(parents=True, exist_ok=True)
                (reports/logname).write_text(run.stdout)
            if run.returncode:
                raise RuntimeError(f'{command[:2]} failed ({run.returncode}):\n{run.stdout[-8000:]}')
        got, wanted = digest(binary), digest(expected)
        if got != wanted:
            raise ValueError(f'Rebuilt binary mismatch: {got} != {wanted}')
        return {'status':'PASS','archiveName':archive.name,'archiveSHA256':digest(archive),'sourceManifestSHA256':digest(manifest),'sourceManifestEntries':verified,'rebuiltBinarySHA256':got,'testedBinarySHA256':wanted,'checks':['safe extraction','source manifest','offline clean-directory Go+cgo/OS SQLite build','uncached Go tests including API contract','byte-identical Linux release binary'],'environment':platform.platform() + '; ' + subprocess.check_output(['go','version'],text=True).strip() + '; only this native target validated'}

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('archive', type=Path)
    parser.add_argument('--expected-binary', required=True, type=Path)
    parser.add_argument('--reports', type=Path)
    args = parser.parse_args()
    result = verify(args.archive.resolve(), args.expected_binary.resolve(), args.reports)
    if args.reports:
        # Embedding an archive's own hash in that archive is impossible. The
        # packaged report binds code-manifest and binary; stdout additionally
        # reports the exact ZIP hash for independent external verification.
        embedded = {k:v for k,v in result.items() if k != 'archiveSHA256'}
        (args.reports/'source-package-verification.json').write_text(json.dumps(embedded, indent=2)+'\n')
    print(json.dumps(result, indent=2))
