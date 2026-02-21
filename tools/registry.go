package tools

import (
	"encoding/json"
	"sync/atomic"
)

// Property describes a JSON Schema property for a tool parameter.
type Property struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// Parameters describes the JSON Schema for tool function parameters.
type Parameters struct {
	Type       string              `json:"type"`
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required"`
}

// Function describes a callable function exposed as a tool.
type Function struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  Parameters `json:"parameters"`
}

// Definition is an OpenAI-compatible tool definition.
type Definition struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Tool binds a definition with its execution logic.
type Tool struct {
	Def     Definition
	Execute func(args json.RawMessage) (string, error)
}

var registry = map[string]*Tool{}

// SubAgentFn is set by main to allow tools to make sub-agent AI calls.
// Depth tracking and display are handled by the implementation in main.
var SubAgentFn func(systemPrompt, userMessage string) (string, error)

// SubAgentDepth tracks the current nesting level of sub-agent calls.
var SubAgentDepth atomic.Int32

// ImapSummarizePrompt is the prompt for imap_summarize_message, set by main via installToolPrompts.
var ImapSummarizePrompt string

// ImapDigestPrompt is the prompt for imap_digest_message, set by main via installToolPrompts.
var ImapDigestPrompt string

// Register adds a tool to the global registry.
// Call from init() in tool implementation files.
func Register(t *Tool) {
	registry[t.Def.Function.Name] = t
}

// Get returns a registered tool by name.
func Get(name string) (*Tool, bool) {
	t, ok := registry[name]
	return t, ok
}

// All returns the definitions of all registered tools.
func All() []Definition {
	defs := make([]Definition, 0, len(registry))
	for _, t := range registry {
		defs = append(defs, t.Def)
	}
	return defs
}
