package middlewares

import (
	"bytes"
	"log/slog"
	"sync"
)

// lockedBuffer is a buffer the logger can write to while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func newTestLogger(w *lockedBuffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil))
}
