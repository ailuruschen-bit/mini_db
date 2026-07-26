# minidb developer tasks.
#
# `make check` is the gate: run it before pushing, and CI runs the same target
# so local and CI results never diverge.
#
# `make check`/`make lint` require golangci-lint on PATH:
#   go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
#   (ensure "$(go env GOPATH)/bin" is on PATH)

.PHONY: check vet lint test race cover build tidy

# The gate: vet, lint, then the full race-enabled, coverage-reporting test suite.
check: vet lint
	go test -race -cover ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

# Plain, fast test run (no race instrumentation).
test:
	go test ./...

# Race detector only.
race:
	go test -race ./...

# Race detector plus a per-package coverage summary.
cover:
	go test -race -cover ./...

build:
	go build ./...

tidy:
	go mod tidy
