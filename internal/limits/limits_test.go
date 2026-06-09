package limits

import (
	"testing"
	"time"
)

func TestPerUserWindowAllowsUpToLimit(t *testing.T) {
	now := time.Unix(0, 0)
	g := New(2)
	for i := 0; i < 2; i++ {
		if !g.AllowAt("u1", now) {
			t.Fatalf("request %d for u1 should be allowed", i)
		}
	}
	if g.AllowAt("u1", now) {
		t.Fatal("3rd request in the same window must be denied")
	}
	// A different user has an independent budget.
	if !g.AllowAt("u2", now) {
		t.Fatal("u2 must have its own budget")
	}
}

func TestWindowResetsNextMinute(t *testing.T) {
	g := New(1)
	now := time.Unix(0, 0)
	if !g.AllowAt("u1", now) {
		t.Fatal("first allowed")
	}
	if g.AllowAt("u1", now) {
		t.Fatal("second denied in same minute")
	}
	if !g.AllowAt("u1", now.Add(time.Minute)) {
		t.Fatal("allowed again next minute")
	}
}

func TestZeroLimitIsUnlimited(t *testing.T) {
	g := New(0)
	now := time.Unix(0, 0)
	for i := 0; i < 100; i++ {
		if !g.AllowAt("u1", now) {
			t.Fatal("limit 0 means unlimited")
		}
	}
}
