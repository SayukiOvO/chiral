package xray

import (
	"fmt"
	"strings"
	"testing"
)

func TestTailKeepsTheNewestLines(t *testing.T) {
	tail := newStderrTail(3)
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(tail, "line %d\n", i)
	}
	got := tail.Tail(0)
	want := "line 8\nline 9\nline 10"
	if got != want {
		t.Fatalf("Tail() = %q, want %q", got, want)
	}
}

// The bound is the point: a crash-looping kernel writes without limit into a
// buffer held in memory on a node whose real job is elsewhere.
func TestTailStaysBounded(t *testing.T) {
	tail := newStderrTail(5)
	for i := 0; i < 100_000; i++ {
		fmt.Fprintf(tail, "noise %d\n", i)
	}
	if n := len(tail.lines); n != 5 {
		t.Fatalf("buffer holds %d lines, want 5", n)
	}
}

// Xray writes several lines per Write, and a partial line arrives with no
// trailing newline at all.
func TestOneWriteMaySplitIntoSeveralLines(t *testing.T) {
	tail := newStderrTail(10)
	tail.Write([]byte("first\nsecond\nthird"))
	if got := tail.Tail(0); got != "first\nsecond\nthird" {
		t.Fatalf("Tail() = %q", got)
	}
}

func TestResetSeparatesOneStartFromTheLast(t *testing.T) {
	tail := newStderrTail(10)
	tail.Write([]byte("failure from the previous process\n"))
	tail.Reset()
	tail.Write([]byte("this run\n"))
	if got := tail.Tail(0); strings.Contains(got, "previous") {
		t.Fatalf("the previous process's lines survived a reset: %q", got)
	}
}

// Losing the diagnostic tail must never take down the process it is watching:
// exec.Cmd's copier treats a writer error as fatal to the stream.
func TestWriteNeverErrors(t *testing.T) {
	tail := newStderrTail(1)
	n, err := tail.Write([]byte("anything at all\n"))
	if err != nil || n != len("anything at all\n") {
		t.Fatalf("Write() = %d, %v", n, err)
	}
}
