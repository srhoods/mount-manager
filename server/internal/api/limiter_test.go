package api

import (
	"os"
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	l := newLimiter(3, 50*time.Millisecond)
	for i := 0; i < 3; i++ {
		if !l.allow("a") {
			t.Fatal("blocked too early")
		}
		l.fail("a")
	}
	if l.allow("a") {
		t.Fatal("should be blocked after 3 failures")
	}
	if !l.allow("b") {
		t.Fatal("other keys unaffected")
	}
	time.Sleep(60 * time.Millisecond)
	if !l.allow("a") {
		t.Fatal("should recover after window")
	}
	l.fail("a")
	l.reset("a")
	if !l.allow("a") {
		t.Fatal("reset failed")
	}
}

func getenv(k string) string { return os.Getenv(k) }
