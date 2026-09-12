package main

import "net/http"

// The SDK sets "no-cache, no-transform" for both JSON and SSE responses.
// no-cache still permits storage; screenshots and desktop observations require
// no-store. Enforce it when headers are committed, preserving SSE flushing and
// ResponseController access to the underlying writer.
type privateMCPResponseWriter struct {
	http.ResponseWriter
	written bool
}

func (w *privateMCPResponseWriter) WriteHeader(status int) {
	if w.written {
		return
	}
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.written = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *privateMCPResponseWriter) Write(data []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *privateMCPResponseWriter) Flush() {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *privateMCPResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
