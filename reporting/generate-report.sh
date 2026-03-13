#!/usr/bin/env bash
set -euo pipefail

# Ensure GOPATH/bin and Homebrew are on PATH
export PATH="${GOPATH:-$HOME/go}/bin:/opt/homebrew/bin:$PATH"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
RESULTS_DIR="$SCRIPT_DIR/allure-results"
REPORT_DIR="$SCRIPT_DIR/allure-report"
HISTORY_FILE="$SCRIPT_DIR/history.jsonl"
OPEN_BROWSER=false

for arg in "$@"; do
  case "$arg" in
    --open) OPEN_BROWSER=true ;;
    --help|-h)
      echo "Usage: $0 [--open]"
      echo "  --open  Open the HTML report in your browser after generation"
      exit 0
      ;;
  esac
done

# --- Prerequisite checks ---
missing=()

if ! command -v gotestsum &>/dev/null; then
  missing+=("gotestsum  → go install gotest.tools/gotestsum@latest")
fi
if ! command -v allure &>/dev/null; then
  missing+=("allure     → brew install allure")
fi
if ! command -v jq &>/dev/null; then
  missing+=("jq         → brew install jq")
fi

if [ ${#missing[@]} -gt 0 ]; then
  echo "Missing prerequisites:"
  for m in "${missing[@]}"; do
    echo "  $m"
  done
  exit 1
fi

# --- Clean previous results ---
rm -rf "$RESULTS_DIR"
mkdir -p "$RESULTS_DIR"

# --- Run tests via gotestsum ---
echo "Running tests..."
cd "$PROJECT_ROOT"

gotestsum \
  --format testdox \
  --junitfile "$RESULTS_DIR/junit.xml" \
  --jsonfile "$RESULTS_DIR/go-test.json" \
  -- -race -count=1 ./... || true

# --- Parse results ---
if [ ! -f "$RESULTS_DIR/go-test.json" ]; then
  echo "Error: go-test.json not produced"
  exit 1
fi

PASSED=$(jq -s '[.[] | select(.Test != null and .Action == "pass")] | length' "$RESULTS_DIR/go-test.json")
FAILED=$(jq -s '[.[] | select(.Test != null and .Action == "fail")] | length' "$RESULTS_DIR/go-test.json")
SKIPPED=$(jq -s '[.[] | select(.Test != null and .Action == "skip")] | length' "$RESULTS_DIR/go-test.json")
TOTAL=$((PASSED + FAILED + SKIPPED))

# Duration: sum of package-level elapsed times
DURATION=$(jq -s '[.[] | select(.Test == null and .Action == "pass" and .Elapsed != null) | .Elapsed] | add // 0' "$RESULTS_DIR/go-test.json")

TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
COMMIT=$(git -C "$PROJECT_ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")

if [ "$FAILED" -gt 0 ]; then
  STATUS="fail"
else
  STATUS="pass"
fi

# --- Restore Allure history from previous runs ---
HISTORY_DIR="$RESULTS_DIR/history"
mkdir -p "$HISTORY_DIR"

if [ -f "$HISTORY_FILE" ] && [ -s "$HISTORY_FILE" ]; then
  # Build Allure history-trend.json from the last 20 entries
  jq -s '
    .[-20:] | [to_entries[] | {
      buildOrder: (.key + 1),
      reportName: "Test Run",
      data: {
        passed: .value.passed,
        failed: .value.failed,
        skipped: .value.skipped,
        total: .value.total
      }
    }]
  ' "$HISTORY_FILE" > "$HISTORY_DIR/history-trend.json"
fi

# --- Append current run to history ---
jq -nc \
  --arg ts "$TIMESTAMP" \
  --arg commit "$COMMIT" \
  --arg status "$STATUS" \
  --argjson passed "$PASSED" \
  --argjson failed "$FAILED" \
  --argjson skipped "$SKIPPED" \
  --argjson total "$TOTAL" \
  --argjson duration "$DURATION" \
  '{
    timestamp: $ts,
    commit: $commit,
    status: $status,
    passed: $passed,
    failed: $failed,
    skipped: $skipped,
    total: $total,
    duration: $duration
  }' >> "$HISTORY_FILE"

# --- Copy allure config ---
if [ -f "$SCRIPT_DIR/allure-config.yml" ]; then
  cp "$SCRIPT_DIR/allure-config.yml" "$RESULTS_DIR/environment.properties" 2>/dev/null || true
fi

# Write environment info for Allure
cat > "$RESULTS_DIR/environment.properties" <<EOF
Commit=$COMMIT
Timestamp=$TIMESTAMP
Go.Version=$(go version | awk '{print $3}')
OS=$(uname -s)
Arch=$(uname -m)
EOF

# --- Generate Allure report ---
echo "Generating Allure report..."
allure generate "$RESULTS_DIR" -o "$REPORT_DIR" --clean 2>/dev/null

echo ""
echo "Results: $PASSED passed, $FAILED failed, $SKIPPED skipped ($TOTAL total) in ${DURATION}s"
echo "Report:  $REPORT_DIR/index.html"

if [ "$OPEN_BROWSER" = true ]; then
  if command -v open &>/dev/null; then
    open "$REPORT_DIR/index.html"
  elif command -v xdg-open &>/dev/null; then
    xdg-open "$REPORT_DIR/index.html"
  else
    echo "Could not detect browser opener. Open the report manually."
  fi
fi
