package tools

import (
	"encoding/json"
	"strings"
	"sync"
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
	Parameters  any        `json:"parameters"`
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

// pendingImages allows tools to return images alongside text results.
// Key: goroutineID, Value: []string (data URIs).
var pendingImages sync.Map

// SetPendingImages stores image data URIs for the calling goroutine.
// Called by tools that produce images (e.g. ha_camera_snapshot).
func SetPendingImages(images []string) {
	pendingImages.Store(goroutineID(), images)
}

// TakePendingImages retrieves and removes any images set by the last tool call.
// Called by the execution loop after each tool execution.
func TakePendingImages() []string {
	v, ok := pendingImages.LoadAndDelete(goroutineID())
	if !ok {
		return nil
	}
	return v.([]string)
}

// All returns the definitions of all registered tools.
// Tools are filtered by prefix based on per-goroutine availability.
func All() []Definition {
	hideImap := !ImapAvailable()
	hideHA := !HAAvailable()
	hideCal := !CalendarAvailable()
	hideCalWrite := hideCal || !CalendarWritable()
	hideContacts := !ContactsAvailable()
	hideContactsWrite := hideContacts || !ContactsWritable()

	defs := make([]Definition, 0, len(registry))
	for _, t := range registry {
		name := t.Def.Function.Name
		if hideImap && strings.HasPrefix(name, "imap_") {
			continue
		}
		if hideHA && strings.HasPrefix(name, "ha_") {
			continue
		}
		if hideCal && strings.HasPrefix(name, "cal_") {
			continue
		}
		if hideContacts && strings.HasPrefix(name, "contacts_") {
			continue
		}
		// Hide write-only tools when not writable
		if hideCalWrite && isCalWriteTool(name) {
			continue
		}
		if hideContactsWrite && isContactsWriteTool(name) {
			continue
		}
		defs = append(defs, t.Def)
	}
	return defs
}

func isCalWriteTool(name string) bool {
	return name == "cal_create_event" || name == "cal_update_event" || name == "cal_delete_event"
}

func isContactsWriteTool(name string) bool {
	return name == "contacts_create" || name == "contacts_update" || name == "contacts_delete"
}
