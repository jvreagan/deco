#!/usr/bin/env bash
# Run tests and generate an Allure HTML report with trend history.
set -euo pipefail
exec "$(dirname "$0")/../reporting/generate-report.sh" "$@"
