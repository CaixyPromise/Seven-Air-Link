#!/usr/bin/env python3
"""Package one natively compiled platform into a versioned release ZIP.

No dependencies outside Python standard library. Never packages credentials or runtime data.
"""
import argparse
import hashlib
import json
import pathlib
import re
import subprocess
import sys
import zipfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
ALLOWED = {"darwin-arm64", "darwin-amd64", "windows-amd64", "linux-amd64", "linux-arm64"}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--platform', required=True, choices=sorted(ALLOWED))
    parser.add_argument('--ref', required=True)
    parser.add_argument('--is-tag', required=True, choices=('true', 'false'))
    opt = parser.parse_args()

    extension = '.exe' if opt.platform.startswith('windows') else ''
    binary = ROOT / 'dist' / ('seven-air-link-' + opt.platform + extension)
    if not binary.is_file():
        parser.error(f'missing compiled binary: {binary}')

    raw = subprocess.check_output([str(binary), 'version'], cwd=ROOT, text=True)
    metadata = json.loads(raw)
    version = metadata['version']
    requested_os, requested_arch = opt.platform.split('-', 1)
    if (metadata.get('os'), metadata.get('arch')) != (requested_os, requested_arch):
        parser.error(f'wrong target: binary reports {metadata.get("os")}/{metadata.get("arch")}, expected {opt.platform}')
    if opt.is_tag == 'true' and (not re.fullmatch(r'\d+\.\d+\.\d+-rc\.\d+', opt.ref) or opt.ref != version):
        parser.error(f'tag {opt.ref!r} does not match compiled program version {version}')

    # Build names are derived from the binary's own version, not unchecked branch names.
    stem = f'Seven-Air-Link-{version}-{opt.platform}'
    out = ROOT / 'release'
    out.mkdir(exist_ok=True)
    archive = out / (stem + '.zip')

    content = [('seven-air-link.exe' if extension else 'seven-air-link', binary)]
    if extension:
        dll = ROOT / 'dist/libsqlite3-0.dll'
        if not dll.is_file():
            parser.error(f'missing runtime SQLite DLL: {dll}')
        for dep in sorted((ROOT / 'dist').glob('*.dll')):
            content.append((dep.name, dep))
    for name in ('README.md', 'START-MAC-WINDOWS.md'):
        src = ROOT / name
        if src.exists():
            content.append((name, src))

    manifest = []
    with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=8) as z:
        for name, src in content:
            data = src.read_bytes()
            entry = zipfile.ZipInfo(name)
            entry.compress_type = zipfile.ZIP_DEFLATED
            entry.create_system = 3
            entry.external_attr = ((0o100755 if name.startswith('seven-air-link') else 0o100644) << 16)
            z.writestr(entry, data)
            manifest.append(f'{hashlib.sha256(data).hexdigest()}  {name}')
        z.writestr('SHA256SUMS.txt', '\n'.join(manifest) + '\n')

    h = hashlib.sha256(archive.read_bytes()).hexdigest()
    (out / (archive.name + '.sha256')).write_text(f'{h}  {archive.name}\n')
    print(f'CREATED {archive} ({archive.stat().st_size} bytes), sha256 {h}', flush=True)


if __name__ == '__main__':
    main()
