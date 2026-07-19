# Test Conventions

> Language: **English** | [日本語](Test-Conventions.ja.md)

Global testing rules shared by every module. Per-module test plans live in the matching subdirectory (e.g. [`storage/`](storage/)) and only describe what is specific to that module.

---

## 1. Framework

Standard library only: the `testing` package driven by `go test`. **No third-party assertion or mocking libraries** — this keeps dependencies minimal and the mechanics explicit.

---

## 2. File Location & Naming

- Test files sit **next to the code**, named `<source>_test.go` (e.g. `slot_entry.go` → `slot_entry_test.go`).
- Files ending in `_test.go` are compiled only by `go test`; they never enter a normal `go build` artifact.
- Test functions are `func TestXxx(t *testing.T)`. Use `t.Run(name, ...)` to create named subtests.

---

## 3. Package Choice: White-box vs Black-box

| Package declaration | Kind      | Can access               | Use for                                   |
| :------------------ | :-------- | :----------------------- | :---------------------------------------- |
| `package <pkg>`     | white-box | unexported members       | byte-layout / internal-structure checks   |
| `package <pkg>_test`| black-box | exported API only        | public-contract / round-trip checks       |

Both are used. Byte-layout tests need the unexported backing array, so they are white-box; tests that assert the public contract should be black-box so they do not couple to internals.

---

## 4. Patterns

### 4.1 Table-driven tests

Enumerate cases in a slice of structs, then loop with `t.Run`. Preferred for bit-field and boundary-value coverage — one table covers many cases.

```go
tests := []struct {
    name string
    in   uint16
    want uint16
}{
    {"zero", 0, 0},
    {"max", 32767, 32767},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) { /* ... */ })
}
```

### 4.2 Golden byte-layout assertions

For any type mapped onto raw bytes, hard-code the expected raw bytes and compare against them. This is the only way to catch a wrong offset or wrong byte order — getter/setter pairs that share the same wrong offset would still agree with each other.

### 4.3 What every byte-mapped type must cover

- **Round-trip**: `set` then `get` returns the same value.
- **Field independence**: writing one field must not corrupt adjacent fields (critical for bit-packed types).
- **Boundary values**: `0`, the field maximum, and one-past-maximum.
- **Error path**: sub-width setters must reject out-of-range input.

---

## 5. Commands

```bash
go test ./...                          # all tests
go test -v ./internal/storage/         # verbose, one line per case
go test -run TestSlotEntry ./internal/storage/   # filter by name
go test -cover ./internal/storage/     # coverage percentage
go test -race ./...                    # data-race detector (for concurrent code)
```

---

## 6. Branch & Commit

- Tests are first-class code: they are **always committed and merged** alongside the code they cover — never left uncommitted.
- Write tests on the **same feature branch** as the code under test; do not open a separate test-only branch.
- Commit with the Conventional Commits `test` type, e.g. `test(storage): cover slot entry bit packing`.
