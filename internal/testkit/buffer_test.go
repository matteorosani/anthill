package testkit_test

import (
	"sync"
	"testing"

	"github.com/matteorosani/anthill/internal/testkit"
)

func TestSyncBufferConcurrentWrites(t *testing.T) {
	var b testkit.SyncBuffer
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = b.Write([]byte("x"))
		}()
	}
	wg.Wait()
	if got := b.String(); got != "xxxxxxxxxx" {
		t.Errorf("buffer = %q", got)
	}
}
