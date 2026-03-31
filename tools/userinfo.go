package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// UserInfoEntry represents a single user setting.
type UserInfoEntry struct {
	Value    string `json:"value"`
	InPrompt bool   `json:"in_prompt"`
	OnlyFor  string `json:"only_for,omitempty"`
}

// userInfoConfig holds per-goroutine userinfo state.
type userInfoConfig struct {
	Path     string // file path to userinfo JSON
	Username string // user's key inside the file
}

var userInfoOverrides sync.Map // goroutineID → *userInfoConfig
var userInfoMu sync.Map       // filepath → *sync.Mutex

// SetUserInfoOverride sets the userinfo config for the current goroutine.
func SetUserInfoOverride(path, username string) {
	userInfoOverrides.Store(goroutineID(), &userInfoConfig{Path: path, Username: username})
}

// ClearUserInfoOverride removes the userinfo config for the current goroutine.
func ClearUserInfoOverride() {
	userInfoOverrides.Delete(goroutineID())
}

// UserInfoAvailable returns true if userinfo is configured for the current goroutine.
func UserInfoAvailable() bool {
	_, ok := userInfoOverrides.Load(goroutineID())
	return ok
}

func getUserInfoConfig() *userInfoConfig {
	v, ok := userInfoOverrides.Load(goroutineID())
	if !ok {
		return nil
	}
	return v.(*userInfoConfig)
}

func userInfoFileMu(path string) *sync.Mutex {
	v, _ := userInfoMu.LoadOrStore(path, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// readUserInfoAll reads the full userinfo file. Caller must hold the file mutex.
func readUserInfoAll(path string) (map[string]map[string]UserInfoEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]map[string]UserInfoEntry{}, nil
		}
		return nil, err
	}
	var all map[string]map[string]UserInfoEntry
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("parse userinfo: %w", err)
	}
	return all, nil
}

// writeUserInfoAll writes the full userinfo file. Caller must hold the file mutex.
func writeUserInfoAll(path string, all map[string]map[string]UserInfoEntry) error {
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// userInfoGet returns entries for the current user.
func userInfoGet(cfg *userInfoConfig) (map[string]UserInfoEntry, error) {
	mu := userInfoFileMu(cfg.Path)
	mu.Lock()
	defer mu.Unlock()
	all, err := readUserInfoAll(cfg.Path)
	if err != nil {
		return nil, err
	}
	entries := all[cfg.Username]
	if entries == nil {
		return map[string]UserInfoEntry{}, nil
	}
	return entries, nil
}

// userInfoSet sets a single entry for the current user (read-modify-write).
func userInfoSet(cfg *userInfoConfig, key string, entry UserInfoEntry) error {
	mu := userInfoFileMu(cfg.Path)
	mu.Lock()
	defer mu.Unlock()
	all, err := readUserInfoAll(cfg.Path)
	if err != nil {
		return err
	}
	if all[cfg.Username] == nil {
		all[cfg.Username] = map[string]UserInfoEntry{}
	}
	all[cfg.Username][key] = entry
	return writeUserInfoAll(cfg.Path, all)
}

// userInfoDelete deletes a single entry for the current user.
func userInfoDelete(cfg *userInfoConfig, key string) error {
	mu := userInfoFileMu(cfg.Path)
	mu.Lock()
	defer mu.Unlock()
	all, err := readUserInfoAll(cfg.Path)
	if err != nil {
		return err
	}
	if all[cfg.Username] != nil {
		delete(all[cfg.Username], key)
	}
	return writeUserInfoAll(cfg.Path, all)
}

// UserInfoPromptBlock returns a text block to inject into the system prompt.
// It includes all entries with in_prompt=true and only_for="" (global),
// plus entries whose only_for matches one of the activeModules.
func UserInfoPromptBlock(activeModules []string) string {
	cfg := getUserInfoConfig()
	if cfg == nil {
		return ""
	}
	entries, err := userInfoGet(cfg)
	if err != nil || len(entries) == 0 {
		return ""
	}

	// Build a set of active module names (lowercased)
	moduleSet := make(map[string]bool, len(activeModules))
	for _, m := range activeModules {
		moduleSet[strings.ToLower(m)] = true
	}

	// Separate: global (in_prompt, no onlyFor) vs per-module
	var globalLines []string
	perModule := map[string][]string{} // onlyFor → lines

	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		e := entries[k]
		if e.OnlyFor != "" {
			// Include only if the module is active
			if moduleSet[strings.ToLower(e.OnlyFor)] {
				perModule[e.OnlyFor] = append(perModule[e.OnlyFor], fmt.Sprintf("  %s: %s", k, e.Value))
			}
		} else if e.InPrompt {
			globalLines = append(globalLines, fmt.Sprintf("  %s: %s", k, e.Value))
		}
	}

	if len(globalLines) == 0 && len(perModule) == 0 {
		return ""
	}

	var sb strings.Builder
	if len(globalLines) > 0 {
		sb.WriteString("\n\nUser info:\n")
		sb.WriteString(strings.Join(globalLines, "\n"))
	}

	// Sort module names for deterministic output
	moduleNames := make([]string, 0, len(perModule))
	for m := range perModule {
		moduleNames = append(moduleNames, m)
	}
	sort.Strings(moduleNames)

	for _, m := range moduleNames {
		sb.WriteString(fmt.Sprintf("\n\nUser info (%s):\n", m))
		sb.WriteString(strings.Join(perModule[m], "\n"))
	}

	return sb.String()
}

// UserInfoForModule returns key→value pairs for a given module name.
// Useful for commands that need to access their onlyFor settings.
func UserInfoForModule(module string) map[string]string {
	cfg := getUserInfoConfig()
	if cfg == nil {
		return nil
	}
	entries, err := userInfoGet(cfg)
	if err != nil {
		return nil
	}
	result := map[string]string{}
	moduleLower := strings.ToLower(module)
	for k, e := range entries {
		if strings.ToLower(e.OnlyFor) == moduleLower {
			result[k] = e.Value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// --- Tool registration ---

func init() {
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "userinfo_set",
				Description: "Set a user preference/setting. Use in_prompt=true to include it in the system prompt on every request. Use only_for to include it only when a specific skill, MCP server, or command is active.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"key":       {Type: "string", Description: "Setting name (e.g. 'timezone', 'username', 'language')"},
						"value":     {Type: "string", Description: "Setting value (e.g. 'Europe/Prague', 'anton')"},
						"in_prompt": {Type: "boolean", Description: "If true, this setting is always shown in the system prompt. Default: false."},
						"only_for":  {Type: "string", Description: "If set, include this setting in the system prompt only when this skill/MCP/command is active (e.g. 'eat', 'github', 'nutricalc'). Implies in-prompt behavior for that context. Default: empty."},
					},
					Required: []string{"key", "value"},
				},
			},
		},
		Execute: executeUserInfoSet,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "userinfo_get",
				Description: "Get a specific user setting by key. Returns the value and metadata (in_prompt, only_for). Works for all settings regardless of in_prompt/only_for.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"key": {Type: "string", Description: "Setting name to retrieve"},
					},
					Required: []string{"key"},
				},
			},
		},
		Execute: executeUserInfoGet,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "userinfo_list",
				Description: "List all user settings. By default returns only keys. Set full=true to include values and metadata.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"full": {Type: "boolean", Description: "If true, return full details (value, in_prompt, only_for) for each key. Default: false (keys only)."},
					},
				},
			},
		},
		Execute: executeUserInfoList,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "userinfo_delete",
				Description: "Delete a user setting by key.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"key": {Type: "string", Description: "Setting name to delete"},
					},
					Required: []string{"key"},
				},
			},
		},
		Execute: executeUserInfoDelete,
	})
}

// --- Tool handlers ---

func executeUserInfoSet(args json.RawMessage) (string, error) {
	cfg := getUserInfoConfig()
	if cfg == nil {
		return "", fmt.Errorf("userinfo not configured for this user")
	}

	var p struct {
		Key      string `json:"key"`
		Value    string `json:"value"`
		InPrompt *bool  `json:"in_prompt"`
		OnlyFor  string `json:"only_for"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p.Key == "" {
		return "", fmt.Errorf("key is required")
	}
	if p.Value == "" {
		return "", fmt.Errorf("value is required")
	}

	inPrompt := false
	if p.InPrompt != nil {
		inPrompt = *p.InPrompt
	}

	entry := UserInfoEntry{
		Value:    p.Value,
		InPrompt: inPrompt,
		OnlyFor:  p.OnlyFor,
	}

	if err := userInfoSet(cfg, p.Key, entry); err != nil {
		return "", fmt.Errorf("save userinfo: %w", err)
	}

	desc := fmt.Sprintf("Saved: %s = %q", p.Key, p.Value)
	if inPrompt {
		desc += " [in system prompt]"
	}
	if p.OnlyFor != "" {
		desc += fmt.Sprintf(" [only for %s]", p.OnlyFor)
	}
	return desc, nil
}

func executeUserInfoGet(args json.RawMessage) (string, error) {
	cfg := getUserInfoConfig()
	if cfg == nil {
		return "", fmt.Errorf("userinfo not configured for this user")
	}

	var p struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p.Key == "" {
		return "", fmt.Errorf("key is required")
	}

	entries, err := userInfoGet(cfg)
	if err != nil {
		return "", err
	}

	e, ok := entries[p.Key]
	if !ok {
		return fmt.Sprintf("Key %q not found.", p.Key), nil
	}

	result := fmt.Sprintf("%s = %s", p.Key, e.Value)
	if e.InPrompt {
		result += "\n  in_prompt: true"
	}
	if e.OnlyFor != "" {
		result += fmt.Sprintf("\n  only_for: %s", e.OnlyFor)
	}
	return result, nil
}

func executeUserInfoList(args json.RawMessage) (string, error) {
	cfg := getUserInfoConfig()
	if cfg == nil {
		return "", fmt.Errorf("userinfo not configured for this user")
	}

	var p struct {
		Full *bool `json:"full"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	entries, err := userInfoGet(cfg)
	if err != nil {
		return "", err
	}

	if len(entries) == 0 {
		return "No user settings configured.", nil
	}

	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	full := p.Full != nil && *p.Full
	if !full {
		return "Keys: " + strings.Join(keys, ", "), nil
	}

	var lines []string
	for _, k := range keys {
		e := entries[k]
		line := fmt.Sprintf("%s = %s", k, e.Value)
		var flags []string
		if e.InPrompt {
			flags = append(flags, "in_prompt")
		}
		if e.OnlyFor != "" {
			flags = append(flags, "only_for="+e.OnlyFor)
		}
		if len(flags) > 0 {
			line += "  [" + strings.Join(flags, ", ") + "]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

func executeUserInfoDelete(args json.RawMessage) (string, error) {
	cfg := getUserInfoConfig()
	if cfg == nil {
		return "", fmt.Errorf("userinfo not configured for this user")
	}

	var p struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p.Key == "" {
		return "", fmt.Errorf("key is required")
	}

	if err := userInfoDelete(cfg, p.Key); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted: %s", p.Key), nil
}
