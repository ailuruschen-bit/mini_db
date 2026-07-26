# minidb developer tasks.
#
# `make check` is the gate: run it before pushing, and CI runs the same target
# so local and CI results never diverge.

.PHONY: check vet test race cover build tidy

# The gate: vet, then the full race-enabled, coverage-reporting test suite.
check: vet
	go test -race -cover ./...

vet:
	go vet ./...

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
