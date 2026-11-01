package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const testChunk = `data: {"choices":[{"delta":{"content":"hi "}}]}` + "\n\n"

func TestStreamIdleTimeout(t *testing.T) {
	old := streamIdleTimeout
	streamIdleTimeout = 200 * time.Millisecond
	defer func() { streamIdleTimeout = old }()

	// Stalls after the first chunk: the client must give up.
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, testChunk)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer stalled.Close()

	_, err := doStream(stalled.URL, "m", nil, nil, 0, false, io.Discard, thinkDefault)
	if !errors.Is(err, errStreamIdle) {
		t.Fatalf("stalled stream: want errStreamIdle, got %v", err)
	}

	// Slow but steady: total time exceeds the idle limit, each gap does not.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 5 {
			io.WriteString(w, testChunk)
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer slow.Close()

	got, err := doChat(slow.URL, "m", nil, 0, 0, thinkDefault)
	if err != nil {
		t.Fatalf("slow stream: %v", err)
	}
	if got != "hi hi hi hi hi" {
		t.Fatalf("slow stream: got %q", got)
	}
}
