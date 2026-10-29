#!/usr/bin/env bash
# Extra M4 gates after scripts/test.sh. Uses only disposable test fixtures.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "${1:-}" == "" ]]; then echo 'usage: scripts/quality_m4.sh /path/to/published-M3-binary' >&2; exit 2; fi
python3 scripts/migration_m4.py --old-binary "$1" --new-binary bin/seven-air-link | tee reports/migration-m4.log
bash scripts/test_two_linux.sh --binary bin/seven-air-link --soak-seconds 120 | tee reports/two-linux-environments.log
