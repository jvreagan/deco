export PATH := $(shell go env GOPATH)/bin:$(PATH)

.PHONY: build test test-report test-report-open vet

build:
	go build -o deco .

vet:
	go vet ./...

test:
	@if command -v gotestsum >/dev/null 2>&1; then \
		gotestsum --format testdox -- -race -count=1 ./...; \
	else \
		echo "gotestsum not found, falling back to go test"; \
		go test -v -race -count=1 ./...; \
	fi

test-report:
	./reporting/generate-report.sh

test-report-open:
	./reporting/generate-report.sh --open
