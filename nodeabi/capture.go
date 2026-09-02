package nodeabi

import "bytes"

// CappedWriter caps a captured stream; the surplus is dropped, never buffered.
//
// It reports the FULL length of every write even when it stored only a prefix.
// That is the io.Writer contract, and it is load-bearing: os/exec copies a
// child's output through io.Copy, which turns a short count into
// io.ErrShortWrite — so an oversized RESULT document would surface as a crash
// instead of reaching ValidateResult and being reported as stdout_overflow.
type CappedWriter struct {
	// Buf receives the prefix that fits.
	Buf *bytes.Buffer
	// Limit is the most Buf will ever hold, in bytes.
	Limit int
}

// NewCappedWriter caps buf at limit bytes.
func NewCappedWriter(buf *bytes.Buffer, limit int) *CappedWriter {
	return &CappedWriter{Buf: buf, Limit: limit}
}

func (c *CappedWriter) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.Limit - c.Buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		c.Buf.Write(p)
	}
	return n, nil
}
