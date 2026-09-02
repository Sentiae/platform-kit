package nodeabi

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// TestCappedWriter_CrossingBoundaryReportsFullLength pins the io.Writer contract
// on the capture buffers. os/exec copies a child's streams with io.Copy, which
// treats a short count as io.ErrShortWrite and fails the whole run — so a writer
// that reported only what it stored would turn any oversized output into a crash
// instead of letting the ABI validator say what is wrong.
//
// CONTROL: return the truncated length (`return len(p), nil` after the reslice)
// and the io.Copy row below reports io.ErrShortWrite — red.
func TestCappedWriter_CrossingBoundaryReportsFullLength(t *testing.T) {
	t.Run("a single write crossing the cap", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewCappedWriter(&buf, 3)
		n, err := w.Write([]byte("abcde"))
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if n != 5 {
			t.Errorf("returned length: got %d, want 5 (the full slice)", n)
		}
		if got := buf.String(); got != "abc" {
			t.Errorf("stored: got %q, want %q", got, "abc")
		}
	})

	t.Run("two writes straddling the cap", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewCappedWriter(&buf, 3)
		if n, _ := w.Write([]byte("ab")); n != 2 {
			t.Errorf("first write: got %d, want 2", n)
		}
		if n, _ := w.Write([]byte("cdef")); n != 4 {
			t.Errorf("second write: got %d, want 4", n)
		}
		if got := buf.String(); got != "abc" {
			t.Errorf("stored: got %q, want %q", got, "abc")
		}
	})

	t.Run("io.Copy through the capped writer never short-writes", func(t *testing.T) {
		var buf bytes.Buffer
		w := NewCappedWriter(&buf, 4)
		n, err := io.Copy(w, strings.NewReader(strings.Repeat("x", 64)))
		if err != nil {
			t.Fatalf("io.Copy: %v (a short count becomes io.ErrShortWrite)", err)
		}
		if n != 64 {
			t.Errorf("copied: got %d, want 64", n)
		}
		if buf.Len() != 4 {
			t.Errorf("stored: got %d bytes, want 4", buf.Len())
		}
	})
}
