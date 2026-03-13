#!/usr/bin/env bash
set -euo pipefail

export PATH="${GOPATH:-$HOME/go}/bin:$PATH"

if ! command -v gotestsum &>/dev/null; then
  echo "gotestsum not found. Install with: go install gotest.tools/gotestsum@latest"
  exit 1
fi

gotestsum --format testdox -- -race -count=1 ./...
