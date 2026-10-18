package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeMCPServer answers initialize, tools/list and tools/call, and hands out a
// fresh Mcp-Session-Id on every response so the session field is written on
// every call. That is what used to race.
func fakeMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	var n int
	var mu sync.Mutex

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		mu.Lock()
		n++
		w.Header().Set("Mcp-Session-Id", fmt.Sprintf("session-%d", n))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		var result string
		switch req.Method {
		case "initialize":
			result = `{"protocolVersion":"2025-03-26"}`
		case "tools/list":
			result = `{"tools":[{"name":"echo","description":"echo","inputSchema":{"type":"object"}}]}`
		case "tools/call":
			result = `{"content":[{"type":"text","text":"ok"}],"isError":false}`
		default:
			result = `{}`
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, result)
	}))
}

func TestMCPServerConcurrentUse(t *testing.T) {
	srv := fakeMCPServer(t)
	defer srv.Close()

	mgr := &MCPManager{servers: map[string]*MCPServer{
		"fake": {name: "fake", cfg: MCPServerConfig{URL: srv.URL, Enabled: true}},
	}}

	// Mimic the bot: many message handlers initializing, listing tool
	// definitions and calling tools at the same time. Under -race this fails
	// if inited, tools or sessionID are touched outside the mutex.
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers*3)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := mgr.InitServers([]string{"fake"}); err != nil {
				errs <- fmt.Errorf("InitServers: %w", err)
				return
			}
			if defs := mgr.ActiveToolDefs([]string{"fake"}, nil); len(defs) != 1 {
				errs <- fmt.Errorf("ActiveToolDefs returned %d defs, want 1", len(defs))
			}
			out, err := mgr.ExecuteTool("fake__echo", json.RawMessage(`{}`))
			if err != nil {
				errs <- fmt.Errorf("ExecuteTool: %w", err)
				return
			}
			if out != "ok" {
				errs <- fmt.Errorf("ExecuteTool returned %q, want \"ok\"", out)
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
