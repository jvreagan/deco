#!/usr/bin/env bash
# Open the latest Allure test report in the browser.
set -euo pipefail

REPORT="$(dirname "$0")/../reporting/allure-report/index.html"

if [ ! -f "$REPORT" ]; then
  echo "No report found. Run scripts/test-report.sh first."
  exit 1
fi

if command -v open &>/dev/null; then
  open "$REPORT"
elif command -v xdg-open &>/dev/null; then
  xdg-open "$REPORT"
else
  echo "Report is at: $REPORT"
fi
