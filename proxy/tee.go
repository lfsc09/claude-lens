package proxy

import (
	"bytes"
	"io"
	"sync"
)

// teeReadCloser wraps a response body so the client streams it live while
// the full contents are buffered for storage. onClose fires exactly once,
// with everything read, when Close is called — which httputil.ReverseProxy
// does once it has finished copying the body to the client or the client
// disconnects.
type teeReadCloser struct {
	io.ReadCloser
	buf     bytes.Buffer
	onClose func([]byte)
	once    sync.Once
}

// newTeeReadCloser wraps rc so onClose receives everything read from it.
func newTeeReadCloser(rc io.ReadCloser, onClose func([]byte)) *teeReadCloser {
	return &teeReadCloser{ReadCloser: rc, onClose: onClose}
}

// Read reads from the wrapped body and copies what it read into the buffer.
func (t *teeReadCloser) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 {
		t.buf.Write(p[:n])
	}
	return n, err
}

// Close closes the wrapped body and, on the first call, hands the buffered
// contents to onClose.
func (t *teeReadCloser) Close() error {
	t.once.Do(func() { t.onClose(t.buf.Bytes()) })
	return t.ReadCloser.Close()
}
