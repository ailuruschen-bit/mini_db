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

For any type mapped onto raw bytes, hard-code the expected raw bytes and compare against them. This is the only way to catch a wrong offset or wrong byte order — getter/setter pairs that share the same wrong offset would still agree with each other. Never recompute the expectation from the same shifts the implementation uses: a formula copied from the code moves in lockstep with a bug in it. Derive the bytes by hand and record the derivation in a comment.

Choose probe values deliberately — they are not arbitrary. Each field's value should be:

- **non-zero**, so "written correctly" is distinguishable from "never written";
- **distinct** from every other field, so a swapped pair is detectable;
- **asymmetric** under byte reversal, so a wrong byte order shows up (`0x1111` would hide it);
- **neither 0 nor the field maximum** for bit-packed fields, and ideally an **alternating bit pattern** — an all-zero or all-one field hides a mask that is off by one bit. For a 2-bit field, `0b10` beats `0b00` and `0b11` because its two bits differ.

A byte ladder such as `0x01020304` is a good probe for multi-byte fields: any reordering or off-by-one shift is visible at a glance. Treat these values as layout probes, not as a semantically valid record — do not try to make the combination meaningful.

### 4.3 What every byte-mapped type must cover

- **Round-trip**: `set` then `get` returns the same value.
- **Field independence**: writing one field must not corrupt adjacent fields (critical for bit-packed types). Run this from **both a zero and an all-max background** — a stray write is only visible when it differs from the background, so a zero background alone is blind to strays that write 0-bits, and a max background alone is blind to strays that write 1-bits. The max background additionally proves a setter really clears bits instead of only OR-ing them in.
- **Boundary values**: `0`, the field maximum, and one-past-maximum.
- **Error path**: sub-width setters must reject out-of-range input.

### 4.4 Concurrent code

Any code with a mutex, a goroutine, or shared mutable state must be tested under `go test -race`, and its test must create real contention — several goroutines hitting the shared state at once, not one after another. Plain assertions can pass by luck when a race happens not to interleave on that run; the race detector flags the unsafe access pattern itself, independent of timing. A concurrency test that never runs under `-race` is close to worthless.

---

## 5. Commands

```bash
go test ./...                          # all tests
go test -v ./internal/storage/...       # verbose, one line per case
go test -run TestSlotEntry ./internal/storage/... # filter by name
go test -cover ./internal/storage/...   # coverage percentage
go test -race ./...                    # data-race detector (for concurrent code)
```

---

## 6. Branch & Commit

- Tests are first-class code: they are **always committed and merged** alongside the code they cover — never left uncommitted.
- Write tests on the **same feature branch** as the code under test; do not open a separate test-only branch.
- Commit with the Conventional Commits `test` type, e.g. `test(storage): cover slot entry bit packing`.
