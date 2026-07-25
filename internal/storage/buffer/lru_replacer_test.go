package buffer

import "testing"

// mustVictim fails unless Victim returns want.
func mustVictim(t *testing.T, r *LRUReplacer, want frameID) {
	t.Helper()
	got, ok := r.Victim()
	if !ok {
		t.Fatalf("Victim: no evictable frame, want %d", want)
	}
	if got != want {
		t.Errorf("Victim = %d, want %d", got, want)
	}
}

// A fresh replacer has no victim.
func TestLRUReplacerEmpty(t *testing.T) {
	r := NewLRUReplacer()
	if _, ok := r.Victim(); ok {
		t.Error("Victim on an empty replacer returned a frame")
	}
	if got := r.Size(); got != 0 {
		t.Errorf("Size = %d, want 0", got)
	}
}

// The least-recently-unpinned frame is evicted first.
func TestLRUReplacerOrder(t *testing.T) {
	r := NewLRUReplacer()
	r.Unpin(1)
	r.Unpin(2)
	r.Unpin(3)

	if got := r.Size(); got != 3 {
		t.Fatalf("Size = %d, want 3", got)
	}
	mustVictim(t, r, 1)
	mustVictim(t, r, 2)
	mustVictim(t, r, 3)
	if _, ok := r.Victim(); ok {
		t.Error("Victim returned a frame after all were evicted")
	}
}

// Pin removes a frame from the candidate set, so it is skipped as a victim.
func TestLRUReplacerPinRemoves(t *testing.T) {
	r := NewLRUReplacer()
	r.Unpin(1)
	r.Unpin(2)
	r.Unpin(3)

	r.Pin(2)
	if got := r.Size(); got != 2 {
		t.Fatalf("Size after Pin = %d, want 2", got)
	}
	mustVictim(t, r, 1)
	mustVictim(t, r, 3)
}

// Unpinning an already-tracked frame must not change its position: recency is
// fixed when the frame first becomes evictable.
func TestLRUReplacerRepeatedUnpinKeepsOrder(t *testing.T) {
	r := NewLRUReplacer()
	r.Unpin(1)
	r.Unpin(2)
	r.Unpin(1) // must not move 1 to the most-recent end

	if got := r.Size(); got != 2 {
		t.Fatalf("Size = %d, want 2 (repeated Unpin must not add a duplicate)", got)
	}
	mustVictim(t, r, 1)
	mustVictim(t, r, 2)
}

// Pinning then unpinning a frame re-enters it at the most-recent end, since it
// was just accessed.
func TestLRUReplacerPinThenUnpinIsMostRecent(t *testing.T) {
	r := NewLRUReplacer()
	r.Unpin(1)
	r.Unpin(2)

	r.Pin(1)   // 1 is used again ...
	r.Unpin(1) // ... and released: now the most recent

	mustVictim(t, r, 2) // 2 is now the oldest
	mustVictim(t, r, 1)
}

// Pinning an untracked frame is a harmless no-op.
func TestLRUReplacerPinUntracked(t *testing.T) {
	r := NewLRUReplacer()
	r.Pin(5) // must not panic
	if got := r.Size(); got != 0 {
		t.Errorf("Size = %d, want 0", got)
	}

	r.Unpin(1)
	r.Pin(9) // pin a frame that is not a candidate
	if got := r.Size(); got != 1 {
		t.Errorf("Size = %d, want 1 (pinning an untracked frame must not affect others)", got)
	}
	mustVictim(t, r, 1)
}
