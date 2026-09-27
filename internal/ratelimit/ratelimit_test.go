package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterRefills(t *testing.T) {
	l := New(60, 2)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.Now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("k"); !ok {
			t.Fatalf("burst %d refused", i)
		}
	}
	ok, wait := l.Allow("k")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("expected refusal with ~1s wait, got %v %v", ok, wait)
	}
	now = now.Add(2 * time.Second)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("token must refill")
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("keys are independent")
	}
}

func TestSweep(t *testing.T) {
	l := New(60, 1)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.Now = func() time.Time { return now }
	for i := 0; i < 999; i++ {
		l.Allow("old")
	}
	now = now.Add(time.Hour)
	l.Allow("new") // 1000th op sweeps
	if _, ok := l.buckets["old"]; ok {
		t.Fatal("idle bucket not swept")
	}
}
