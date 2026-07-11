package middleware

import (
	"testing"
	"time"
)

func TestLimiter_AllowsBurstThenBlocks(t *testing.T) {
	l := New(2, time.Minute)

	ok1, _ := l.allow("key")
	ok2, _ := l.allow("key")
	ok3, _ := l.allow("key")

	if !ok1 || !ok2 {
		t.Fatal("expected first two requests to be allowed")
	}
	if ok3 {
		t.Fatal("expected third request to be blocked")
	}
}

func TestLimiter_DifferentKeysIndependent(t *testing.T) {
	l := New(1, time.Minute)

	okA, _ := l.allow("A")
	okB, _ := l.allow("B")

	if !okA || !okB {
		t.Fatal("different keys should have independent buckets")
	}
}

func TestNew_PanicsOnInvalidInput(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on requests=0")
		}
	}()
	New(0, time.Minute)
}
