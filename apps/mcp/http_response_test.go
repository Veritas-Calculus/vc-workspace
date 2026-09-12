package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrivateMCPResponsePreservesFlushAndOverridesSDKCaching(t *testing.T) {
	for _, flushFirst := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		writer := &privateMCPResponseWriter{ResponseWriter: recorder}
		writer.Header().Set("Cache-Control", "no-cache, no-transform")
		if flushFirst {
			if err := http.NewResponseController(writer).Flush(); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = writer.Write([]byte("data: private-event\n\n"))
		writer.WriteHeader(http.StatusInternalServerError)
		response := recorder.Result()
		if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store, no-transform" || recorder.Body.String() != "data: private-event\n\n" {
			t.Fatal("private response lost headers, status or body")
		}
		if flushFirst && !recorder.Flushed {
			t.Fatal("SSE flush did not reach the underlying writer")
		}
	}
}
