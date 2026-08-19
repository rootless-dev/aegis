package middleware

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

// HeaderRecorder reports whether the response was already committed, so a
// middleware downstream knows it can no longer write its own status.
type HeaderRecorder interface {
	WroteHeader() bool
}

type responseRecorder struct {
	http.ResponseWriter

	status      int
	bytes       int
	wroteHeader bool
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}

	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	written, err := w.ResponseWriter.Write(b)
	w.bytes += written

	return written, err
}

func (w *responseRecorder) WroteHeader() bool {
	return w.wroteHeader
}

// Unwrap exposes the original writer to http.ResponseController.
func (w *responseRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Flush and Hijack are forwarded so that code doing a direct type assertion —
// still common in the ecosystem — keeps working through the wrapper. The
// tradeoff is that the recorder always satisfies both interfaces even when the
// underlying writer does not, which is why Hijack answers with an error
// instead of panicking.
func (w *responseRecorder) Flush() {
	flusher, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}

	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}

	flusher.Flush()
}

func (w *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("middleware: underlying response writer does not support hijacking")
	}

	return hijacker.Hijack()
}
