package testkit

import (
	"bytes"
	"sync"
)

// SyncBuffer is a bytes.Buffer that is safe for concurrent use, such as
// the activity output of a run (RunSpec.Activity).
type SyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p.
func (b *SyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns what was written so far.
func (b *SyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
