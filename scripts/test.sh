#!/usr/bin/env bash
# Run from a development checkout with Go, C compiler (race), and Python 3 + the test-only websockets package (see requirements-test.txt).
set -euo pipefail
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
mkdir -p reports bin
test -z "$(gofmt -l cmd internal web skills)"
go vet ./... >reports/vet.log 2>&1
go test -count=1 -v ./... >reports/unit-tests.log 2>&1
go test -race -count=1 -coverpkg=./... -coverprofile=reports/coverage.out ./... >reports/race-tests.log 2>&1
go tool cover -func=reports/coverage.out >reports/coverage-summary.txt
CGO_ENABLED=1 go build -buildvcs=false -trimpath -ldflags='-s -w' -o bin/seven-air-link ./cmd/seven-air-link
python3 scripts/test_legacy.py >reports/legacy-tests.log 2>&1
python3 scripts/e2e.py --binary bin/seven-air-link >reports/e2e.log 2>&1
python3 scripts/e2e_m2.py --binary bin/seven-air-link >reports/m2-e2e.log 2>&1
python3 scripts/e2e_m3.py --binary bin/seven-air-link >reports/m3-e2e.log 2>&1
python3 scripts/interop_test.py --binary bin/seven-air-link >reports/interop.log 2>&1
printf 'M1 + M2 + M3 regression on SQLite passed. Browser checks are separate.\n'
