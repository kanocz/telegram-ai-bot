package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// UserOption is a single choice presented to the user.
type UserOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// UserQuestion is a question presented to the user with optional choices.
type UserQuestion struct {
	Question    string       `json:"question"`
	Options     []UserOption `json:"options,omitempty"`
	MultiSelect bool         `json:"multi_select,omitempty"`
}

// UserPrompter asks the user a question and returns their answer.
type UserPrompter interface {
	Ask(q UserQuestion) (string, error)
}

// Goroutine-local prompter storage.
var askOverrides sync.Map // goroutineID -> UserPrompter

// SetPrompter stores a UserPrompter for the calling goroutine.
func SetPrompter(p UserPrompter) { askOverrides.Store(goroutineID(), p) }

// ClearPrompter removes the UserPrompter for the calling goroutine.
func ClearPrompter() { askOverrides.Delete(goroutineID()) }

// GetPrompter returns the UserPrompter for the calling goroutine, or nil.
func GetPrompter() UserPrompter {
	v, ok := askOverrides.Load(goroutineID())
	if !ok {
		return nil
	}
	return v.(UserPrompter)
}

// AskAvailable returns true if a UserPrompter is set for the current goroutine.
func AskAvailable() bool {
	_, ok := askOverrides.Load(goroutineID())
	return ok
}

// ask_user tool arguments (parsed from raw JSON).
type askUserArgs struct {
	Question    string `json:"question"`
	MultiSelect bool   `json:"multi_select"`
	// Options is parsed manually because it's an array of objects.
	RawOptions json.RawMessage `json:"options"`
}

func init() {
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name: "ask_user",
				Description: "Ask the user a question and wait for their answer. " +
					"Use this when you need clarification, a choice between options, " +
					"or confirmation before proceeding. You can provide predefined options " +
					"or ask an open-ended question (omit options). " +
					"The user can always provide a custom free-text answer.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{
							"type":        "string",
							"description": "The question to ask the user",
						},
						"options": map[string]any{
							"type":        "array",
							"description": "Optional list of choices. Omit for open-ended questions.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"label": map[string]any{
										"type":        "string",
										"description": "Short display text for this option",
									},
									"description": map[string]any{
										"type":        "string",
										"description": "Explanation of what this option means",
									},
								},
								"required": []string{"label"},
							},
						},
						"multi_select": map[string]any{
							"type":        "boolean",
							"description": "Allow selecting multiple options (default false)",
						},
					},
					"required": []string{"question"},
				},
			},
		},
		Execute: execAskUser,
	})
}

func execAskUser(args json.RawMessage) (string, error) {
	p := GetPrompter()
	if p == nil {
		return "", fmt.Errorf("ask_user is not available in this mode")
	}

	var a askUserArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Question == "" {
		return "", fmt.Errorf("question is required")
	}

	var options []UserOption
	if len(a.RawOptions) > 0 && string(a.RawOptions) != "null" {
		if err := json.Unmarshal(a.RawOptions, &options); err != nil {
			return "", fmt.Errorf("invalid options: %w", err)
		}
	}

	answer, err := p.Ask(UserQuestion{
		Question:    a.Question,
		Options:     options,
		MultiSelect: a.MultiSelect,
	})
	if err != nil {
		return "", fmt.Errorf("ask_user failed: %w", err)
	}

	// Wrap answer in a clear format for the model
	var sb strings.Builder
	sb.WriteString("User answered: ")
	sb.WriteString(answer)
	return sb.String(), nil
}
