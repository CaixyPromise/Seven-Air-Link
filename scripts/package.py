#!/usr/bin/env python3
"""Package a built checkout without runtime secrets, git state, or tool caches.

Build first with scripts/build.sh. The source manifest deliberately excludes
reports and binaries so test artifacts do not recursively hash themselves.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import stat
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
TARGETS = ('linux-amd64',) # M4: only the actually built and tested native target
EXCLUDED = {'.git', 'bin', 'dist', 'release', '__pycache__', '.pytest_cache', '.DS_Store'}
STAMP = (2026, 10, 28, 0, 0, 0)

def sha(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()

def source_files() -> list[Path]:
    result = []
    for path in sorted(ROOT.rglob('*')):
        relative = path.relative_to(ROOT)
        if any(part in EXCLUDED or part.startswith('.slb') for part in relative.parts):
            continue
        if not path.is_file() or path.suffix in ('.pyc', '.pid'):
            continue
        if path.is_symlink():
            raise ValueError(f'Refusing symlink in source package: {relative}')
        if path.name in ('admin.json', 'server.key', 'metadata.json', 'bridge.db', 'manifest.json') or path.name.endswith(('.node.json', '.services.json', '.db-wal', '.db-shm', '.cursor')):
            raise ValueError(f'Refusing runtime data in source package: {relative}')
        result.append(path)
    return result

def put(archive: zipfile.ZipFile, path: Path, relative: str) -> None:
    info = zipfile.ZipInfo('seven-air-link/' + relative, STAMP)
    info.compress_type = zipfile.ZIP_DEFLATED
    info.create_system = 3
    mode = 0o755 if path.stat().st_mode & 0o111 else 0o644
    info.external_attr = (stat.S_IFREG | mode) << 16
    archive.writestr(info, path.read_bytes(), compresslevel=6)

def package(out: Path) -> list[Path]:
    out = out.resolve()
    if out == ROOT or ROOT in out.parents:
        raise ValueError('Output directory must be outside the source checkout')
    out.mkdir(parents=True, exist_ok=True)
    binaries = {target: ROOT/'dist'/('seven-air-link-' + target + ('.exe' if target.startswith('windows') else '')) for target in TARGETS}
    for binary in binaries.values():
        if not binary.is_file():
            raise FileNotFoundError(f'Build first: missing {binary}')
    files = source_files()
    core = [p for p in files if p.relative_to(ROOT).parts[0] != 'reports' and p.name != 'SOURCE-SHA256SUMS']
    manifest = ROOT/'SOURCE-SHA256SUMS'
    manifest.write_text(''.join(f'{sha(p)}  {p.relative_to(ROOT).as_posix()}\n' for p in core))
    files = source_files()
    outputs = []
    for variant in ('source', 'complete', *TARGETS):
        destination = out/f'Seven-Air-Link-M4-{variant}.zip'
        with zipfile.ZipFile(destination, 'w') as archive:
            for path in files:
                put(archive, path, path.relative_to(ROOT).as_posix())
            if variant == 'complete':
                for binary in binaries.values():
                    put(archive, binary, 'dist/' + binary.name)
                put(archive, ROOT/'dist/SHA256SUMS', 'dist/SHA256SUMS')
            elif variant != 'source':
                put(archive, binaries[variant], 'seven-air-link' + ('.exe' if variant.startswith('windows') else ''))
            bad = archive.testzip()
            if bad:
                raise ValueError(f'Corrupt package member: {bad}')
        outputs.append(destination)
    (out/'Seven-Air-Link-M4-SHA256SUMS.txt').write_text(''.join(f'{sha(p)}  {p.name}\n' for p in outputs))
    return outputs

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, default=ROOT.parent)
    args = parser.parse_args()
    outputs = package(args.out)
    print(json.dumps([{'file':str(p),'bytes':p.stat().st_size,'sha256':sha(p)} for p in outputs], indent=2))
