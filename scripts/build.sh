#!/usr/bin/env bash
# Native M4 build. cgo + SQLite headers/static library are BUILD dependencies.
# No download and no claimed cross-compilation without a target C toolchain.
set -euo pipefail
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off CGO_ENABLED=1
mkdir -p dist reports
os=$(go env GOOS); arch=$(go env GOARCH); hostos=$(go env GOHOSTOS); hostarch=$(go env GOHOSTARCH)
if [[ "$os/$arch" != "$hostos/$hostarch" ]]; then
 echo 'This script intentionally builds the native target only; use a reviewed native target toolchain.' >&2; exit 1
fi
suffix=''; [[ "$os" != windows ]] || suffix='.exe'
dest="dist/seven-air-link-${os}-${arch}${suffix}"
{
 go version
 echo "Native target: $os/$arch; CGO_ENABLED=1"
 go build -buildvcs=false -trimpath -ldflags='-s -w' -o "$dest" ./cmd/seven-air-link
 go version -m "$dest"
 [[ "$os" != linux ]] || ldd "$dest"
} > reports/build.log 2>&1
python3 - "$dest" "$os/$arch" <<'PY'
from pathlib import Path
import hashlib,json,sys
p=Path(sys.argv[1]);r={'file':p.name,'bytes':p.stat().st_size,'sha256':hashlib.sha256(p.read_bytes()).hexdigest(),'target':sys.argv[2],'cgo':True,'validation':'native compilation; test outcomes recorded separately'}
Path('dist/SHA256SUMS').write_text(r['sha256']+'  '+r['file']+'\n')
Path('reports/builds.json').write_text(json.dumps({'status':'PASS','builds':[r],'crossPlatformBuilds':[]},indent=2)+'\n')
PY
printf 'Built native %s; no untested platform binaries included.\n' "$os/$arch"
