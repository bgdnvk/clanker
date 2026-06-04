package hermes

import "sync"

// ringBuffer is a small fixed-capacity byte buffer that keeps the most
// recent N bytes written to it. Used to capture the tail of bridge
// stderr so the restart path (clanker-cli #21) can include "what the
// bridge said before it died" in its error message — typically a
// Python traceback ending in something useful like ModuleNotFoundError.
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newRingBuffer(capacity int) *ringBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &ringBuffer{cap: capacity}
}

// Write implements io.Writer. Keeps only the trailing `cap` bytes —
// older content is discarded as new content arrives.
func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.cap {
		r.buf = r.buf[len(r.buf)-r.cap:]
	}
	return len(p), nil
}

// String returns the current trailing contents as a string.
func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
