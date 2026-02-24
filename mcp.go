package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"ai-webfetch/tools"
)

// MCPServerConfig holds the configuration for a single MCP server.
type MCPServerConfig struct {
	URL     string            `json:"url"`
	Enabled bool              `json:"enabled"`
	Headers map[string]string `json:"headers"`
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// MCPServer represents a connection to a single MCP server.
type MCPServer struct {
	name      string
	cfg       MCPServerConfig
	mu        sync.Mutex
	inited    bool
	sessionID string
	tools     []mcpTool
}

// MCPManager manages multiple MCP servers.
type MCPManager struct {
	servers map[string]*MCPServer
}

// LoadMCPConfig reads mcp.json and creates an MCPManager.
func LoadMCPConfig(path string) (*MCPManager, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var configs map[string]MCPServerConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		return nil, err
	}
	mgr := &MCPManager{servers: make(map[string]*MCPServer, len(configs))}
	for name, cfg := range configs {
		mgr.servers[name] = &MCPServer{name: name, cfg: cfg}
	}
	return mgr, nil
}

// InitEnabled initializes all servers marked enabled in config.
func (m *MCPManager) InitEnabled(logf func(string, ...any)) {
	for name, srv := range m.servers {
		if srv.cfg.Enabled {
			if err := srv.initialize(); err != nil {
				logf("MCP %s: init error: %v\n", name, err)
			} else {
				logf("MCP %s: %d tools\n", name, len(srv.tools))
			}
		}
	}
}

// InitServers initializes specific servers by name (lazy init for on-demand).
func (m *MCPManager) InitServers(names []string) error {
	for _, name := range names {
		srv, ok := m.servers[name]
		if !ok {
			available := m.serverNames()
			return fmt.Errorf("unknown MCP server %q (available: %s)", name, strings.Join(available, ", "))
		}
		if err := srv.initialize(); err != nil {
			return fmt.Errorf("MCP %s: %w", name, err)
		}
	}
	return nil
}

// ValidateNames checks that all given names exist in config.
func (m *MCPManager) ValidateNames(names []string) error {
	for _, name := range names {
		if _, ok := m.servers[name]; !ok {
			available := m.serverNames()
			return fmt.Errorf("unknown MCP server %q (available: %s)", name, strings.Join(available, ", "))
		}
	}
	return nil
}

func (m *MCPManager) serverNames() []string {
	names := make([]string, 0, len(m.servers))
	for k := range m.servers {
		names = append(names, k)
	}
	return names
}

// ActiveToolDefs returns tool definitions for enabled + extra servers.
func (m *MCPManager) ActiveToolDefs(extraNames []string) []tools.Definition {
	active := map[string]bool{}
	for name, srv := range m.servers {
		if srv.cfg.Enabled && srv.inited {
			active[name] = true
		}
	}
	for _, name := range extraNames {
		active[name] = true
	}

	var defs []tools.Definition
	for name := range active {
		srv := m.servers[name]
		if !srv.inited {
			continue
		}
		for _, t := range srv.tools {
			defs = append(defs, tools.Definition{
				Type: "function",
				Function: tools.Function{
					Name:        name + "__" + t.Name,
					Description: fmt.Sprintf("[%s] %s", name, t.Description),
					Parameters:  t.InputSchema,
				},
			})
		}
	}
	return defs
}

// ExecuteTool routes a qualified tool name (server__tool) to the correct server.
func (m *MCPManager) ExecuteTool(qualifiedName string, args json.RawMessage) (string, error) {
	parts := strings.SplitN(qualifiedName, "__", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid MCP tool name %q", qualifiedName)
	}
	serverName, toolName := parts[0], parts[1]
	srv, ok := m.servers[serverName]
	if !ok {
		return "", fmt.Errorf("unknown MCP server %q", serverName)
	}
	return srv.callTool(toolName, args)
}

// makeToolExec creates a tool executor that handles both built-in and MCP tools.
func makeToolExec(mcpMgr *MCPManager, mcpNames []string) func(string, json.RawMessage) (string, error) {
	return func(name string, args json.RawMessage) (string, error) {
		if tool, ok := tools.Get(name); ok {
			return tool.Execute(args)
		}
		if mcpMgr != nil && strings.Contains(name, "__") {
			return mcpMgr.ExecuteTool(name, args)
		}
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// --- MCP JSON-RPC protocol ---

type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *MCPServer) initialize() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inited {
		return nil
	}

	// Step 1: initialize
	id1 := 1
	initResp, err := s.rpcCall(&jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id1,
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]string{
				"name":    "ai-webfetch",
				"version": "1.0.0",
			},
		},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if initResp.Error != nil {
		return fmt.Errorf("initialize: %s", initResp.Error.Message)
	}

	// Step 2: notifications/initialized
	_ = s.rpcNotify(&jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})

	// Step 3: tools/list
	id3 := 2
	listResp, err := s.rpcCall(&jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id3,
		Method:  "tools/list",
	})
	if err != nil {
		return fmt.Errorf("tools/list: %w", err)
	}
	if listResp.Error != nil {
		return fmt.Errorf("tools/list: %s", listResp.Error.Message)
	}

	var listResult struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(listResp.Result, &listResult); err != nil {
		return fmt.Errorf("tools/list decode: %w", err)
	}

	s.tools = listResult.Tools
	s.inited = true
	return nil
}

func (s *MCPServer) callTool(toolName string, args json.RawMessage) (string, error) {
	id := 1
	resp, err := s.rpcCall(&jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "tools/call",
		Params: map[string]any{
			"name":      toolName,
			"arguments": json.RawMessage(args),
		},
	})
	if err != nil {
		return "", err
	}
	if resp.Error != nil {
		return "", fmt.Errorf("MCP error: %s", resp.Error.Message)
	}

	var callResult struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp.Result, &callResult); err != nil {
		return "", fmt.Errorf("decode result: %w", err)
	}

	var texts []string
	for _, c := range callResult.Content {
		if c.Type == "text" {
			texts = append(texts, c.Text)
		}
	}
	result := strings.Join(texts, "\n")
	if callResult.IsError {
		return "", fmt.Errorf("tool error: %s", result)
	}
	return result, nil
}

func (s *MCPServer) rpcCall(req *jsonRPCRequest) (*jsonRPCResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest("POST", s.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range s.cfg.Headers {
		httpReq.Header.Set(k, v)
	}
	if s.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", s.sessionID)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, b)
	}

	// Save session ID
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		s.sessionID = sid
	}

	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		return s.parseSSE(resp.Body)
	}

	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &rpcResp, nil
}

func (s *MCPServer) rpcNotify(req *jsonRPCRequest) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequest("POST", s.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range s.cfg.Headers {
		httpReq.Header.Set(k, v)
	}
	if s.sessionID != "" {
		httpReq.Header.Set("Mcp-Session-Id", s.sessionID)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (s *MCPServer) parseSSE(body io.Reader) (*jsonRPCResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var rpcResp jsonRPCResponse
		if err := json.Unmarshal([]byte(data), &rpcResp); err != nil {
			continue
		}
		// Return the first valid JSON-RPC response with an ID (skip notifications)
		if rpcResp.ID != nil {
			return &rpcResp, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("SSE read: %w", err)
	}
	return nil, fmt.Errorf("no JSON-RPC response found in SSE stream")
}
