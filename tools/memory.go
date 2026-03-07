package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// --- Per-goroutine memory path (same pattern as IMAP overrides) ---

var memoryOverrides sync.Map // goroutineID → string (dir path)

func SetMemoryOverride(path string) {
	memoryOverrides.Store(goroutineID(), path)
}

func ClearMemoryOverride() {
	memoryOverrides.Delete(goroutineID())
}

func MemoryAvailable() bool {
	_, ok := memoryOverrides.Load(goroutineID())
	return ok
}

func getMemoryPath() (string, error) {
	v, ok := memoryOverrides.Load(goroutineID())
	if !ok {
		return "", fmt.Errorf("memory not configured")
	}
	return v.(string), nil
}

// --- Data model ---

type memoryEntity struct {
	Type      string           `json:"type"`
	Name      string           `json:"name"`
	Facts     []string         `json:"facts"`
	Tags      []string         `json:"tags,omitempty"`
	Relations []memoryRelation `json:"relations,omitempty"`
	Created   string           `json:"created"`
	Updated   string           `json:"updated"`
}

type memoryRelation struct {
	Rel    string `json:"rel"`
	Target string `json:"target"`
}

type memoryEpisode struct {
	ID      string   `json:"id"`
	Time    string   `json:"ts"`
	Type    string   `json:"type,omitempty"`
	Context string   `json:"context,omitempty"`
	Summary string   `json:"summary"`
	Tags    []string `json:"tags,omitempty"`
}

type memoryData struct {
	Entities map[string]*memoryEntity `json:"entities"`
	Episodes []memoryEpisode          `json:"episodes"`
}

const maxEpisodes = 500

// --- File I/O with per-path mutex ---

var memoryMu sync.Map // dir → *sync.Mutex

func memLock(dir string) *sync.Mutex {
	v, _ := memoryMu.LoadOrStore(dir, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func loadMemoryData(dir string) (*memoryData, error) {
	path := filepath.Join(dir, "memory.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &memoryData{Entities: map[string]*memoryEntity{}}, nil
		}
		return nil, err
	}
	var store memoryData
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse memory.json: %w", err)
	}
	if store.Entities == nil {
		store.Entities = map[string]*memoryEntity{}
	}
	return &store, nil
}

func saveMemoryData(dir string, store *memoryData) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if len(store.Episodes) > maxEpisodes {
		store.Episodes = store.Episodes[len(store.Episodes)-maxEpisodes:]
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "memory.json"), data, 0644)
}

// --- Tool registration ---

func init() {
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name: "memory_store",
				Description: `Store a memory. Two modes:
1. Entity: set entity_id + name + facts to create/update a persistent record (contact, topic, preference, etc). Facts are merged on update.
2. Episode: set episode_summary to log a timestamped event. Link to an entity via context.
Both can be combined in one call.`,
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"entity_id":       {Type: "string", Description: "Entity key, e.g. 'contact:user@example.com', 'topic:ai-regulation', 'pref:language'"},
						"type":            {Type: "string", Description: "Entity type: contact, topic, preference, project, etc."},
						"name":            {Type: "string", Description: "Entity display name"},
						"facts":           {Type: "string", Description: `Facts as JSON array: ["works at Google", "prefers English"]. Merged with existing.`},
						"tags":            {Type: "string", Description: `Tags as JSON array: ["work", "email"]. Merged with existing.`},
						"add_relation":    {Type: "string", Description: `Add relation as JSON: {"rel":"discussed","target":"topic:ai"}`},
						"episode_summary": {Type: "string", Description: "Event description to log with timestamp"},
						"episode_type":    {Type: "string", Description: "Episode category: chat, email, news (default: chat)"},
						"context":         {Type: "string", Description: "Entity ID to link this episode to"},
					},
				},
			},
		},
		Execute: execMemStore,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name: "memory_search",
				Description: "Search memories by text (substring in names, facts, summaries), entity type, or tags. Returns matching entities and episodes, newest first.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"query": {Type: "string", Description: "Search text (case-insensitive substring)"},
						"type":  {Type: "string", Description: "Filter by type (contact, topic, preference, chat, email, news, etc.)"},
						"tags":  {Type: "string", Description: `Filter by tags as JSON array (AND logic): ["work"]`},
						"limit": {Type: "integer", Description: "Max results (default: 20)"},
					},
				},
			},
		},
		Execute: execMemSearch,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name: "memory_recall",
				Description: "Get full details of an entity: all facts, relations, and linked episodes.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"entity_id": {Type: "string", Description: "Entity ID to recall"},
					},
					Required: []string{"entity_id"},
				},
			},
		},
		Execute: execMemRecall,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name: "memory_forget",
				Description: "Delete an entity (with all linked episodes) or a specific episode by ID.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"entity_id":  {Type: "string", Description: "Entity ID to delete (also removes linked episodes)"},
						"episode_id": {Type: "string", Description: "Specific episode ID to delete"},
					},
				},
			},
		},
		Execute: execMemForget,
	})

	// --- Session-scoped temporary storage ---

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "memory_temp_put",
				Description: "Save a key-value pair for the current session. Useful for accumulating data across multiple tool calls (e.g. collecting facts from several emails, tracking news topics across sources). Lost when session ends.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"key":   {Type: "string", Description: "Storage key (e.g. 'email_summary_ivan', 'news_topic_1')"},
						"value": {Type: "string", Description: "Value to store (any text)"},
					},
					Required: []string{"key", "value"},
				},
			},
		},
		Execute: execTempPut,
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "memory_temp_get",
				Description: "Retrieve session-scoped values. Without key, returns all stored pairs. Useful for recalling accumulated data before final synthesis.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"key": {Type: "string", Description: "Key to retrieve (omit to list all)"},
					},
				},
			},
		},
		Execute: execTempGet,
	})
}

// --- Tool implementations ---

func execMemStore(rawArgs json.RawMessage) (string, error) {
	dir, err := getMemoryPath()
	if err != nil {
		return "", err
	}

	var args struct {
		EntityID       string `json:"entity_id"`
		Type           string `json:"type"`
		Name           string `json:"name"`
		Facts          string `json:"facts"`
		Tags           string `json:"tags"`
		AddRelation    string `json:"add_relation"`
		EpisodeSummary string `json:"episode_summary"`
		EpisodeType    string `json:"episode_type"`
		Context        string `json:"context"`
	}
	json.Unmarshal(rawArgs, &args)

	mu := memLock(dir)
	mu.Lock()
	defer mu.Unlock()

	store, err := loadMemoryData(dir)
	if err != nil {
		return "", err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var parts []string

	// Episode mode
	if args.EpisodeSummary != "" {
		epType := args.EpisodeType
		if epType == "" {
			epType = "chat"
		}
		ep := memoryEpisode{
			ID:      fmt.Sprintf("ep_%d", time.Now().UnixNano()),
			Time:    now,
			Type:    epType,
			Context: args.Context,
			Summary: args.EpisodeSummary,
		}
		if args.Tags != "" {
			json.Unmarshal([]byte(args.Tags), &ep.Tags)
		}
		store.Episodes = append(store.Episodes, ep)
		parts = append(parts, fmt.Sprintf("Episode stored: %s", ep.ID))
	}

	// Entity mode
	if args.EntityID != "" {
		e, exists := store.Entities[args.EntityID]
		if !exists {
			e = &memoryEntity{Created: now}
			store.Entities[args.EntityID] = e
		}
		if args.Type != "" {
			e.Type = args.Type
		}
		if args.Name != "" {
			e.Name = args.Name
		}
		if args.Facts != "" {
			var newFacts []string
			json.Unmarshal([]byte(args.Facts), &newFacts)
			e.Facts = mergeStrings(e.Facts, newFacts)
		}
		if args.Tags != "" {
			var newTags []string
			json.Unmarshal([]byte(args.Tags), &newTags)
			e.Tags = mergeStrings(e.Tags, newTags)
		}
		if args.AddRelation != "" {
			var rel memoryRelation
			json.Unmarshal([]byte(args.AddRelation), &rel)
			if rel.Rel != "" && rel.Target != "" {
				e.Relations = append(e.Relations, rel)
			}
		}
		e.Updated = now
		verb := "updated"
		if !exists {
			verb = "created"
		}
		parts = append(parts, fmt.Sprintf("Entity %s: %s", verb, args.EntityID))
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("provide entity_id or episode_summary")
	}
	if err := saveMemoryData(dir, store); err != nil {
		return "", fmt.Errorf("save: %w", err)
	}
	return strings.Join(parts, "\n"), nil
}

func execMemSearch(rawArgs json.RawMessage) (string, error) {
	dir, err := getMemoryPath()
	if err != nil {
		return "", err
	}

	var args struct {
		Query string `json:"query"`
		Type  string `json:"type"`
		Tags  string `json:"tags"`
		Limit int    `json:"limit"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Limit <= 0 {
		args.Limit = 20
	}

	mu := memLock(dir)
	mu.Lock()
	store, err := loadMemoryData(dir)
	mu.Unlock()
	if err != nil {
		return "", err
	}

	q := strings.ToLower(args.Query)
	var filterTags []string
	if args.Tags != "" {
		json.Unmarshal([]byte(args.Tags), &filterTags)
	}

	var sb strings.Builder
	count := 0

	// Entities sorted by updated time (newest first)
	type entityEntry struct {
		id string
		e  *memoryEntity
	}
	var entities []entityEntry
	for id, e := range store.Entities {
		entities = append(entities, entityEntry{id, e})
	}
	sort.Slice(entities, func(i, j int) bool {
		return entities[i].e.Updated > entities[j].e.Updated
	})

	for _, ee := range entities {
		if count >= args.Limit {
			break
		}
		if args.Type != "" && ee.e.Type != args.Type {
			continue
		}
		if len(filterTags) > 0 && !memHasAllTags(ee.e.Tags, filterTags) {
			continue
		}
		if q != "" && !entityMatchesQuery(ee.id, ee.e, q) {
			continue
		}
		sb.WriteString(fmtEntity(ee.id, ee.e))
		count++
	}

	// Episodes newest first
	for i := len(store.Episodes) - 1; i >= 0 && count < args.Limit; i-- {
		ep := store.Episodes[i]
		if args.Type != "" && ep.Type != args.Type {
			continue
		}
		if len(filterTags) > 0 && !memHasAllTags(ep.Tags, filterTags) {
			continue
		}
		if q != "" &&
			!strings.Contains(strings.ToLower(ep.Summary), q) &&
			!strings.Contains(strings.ToLower(ep.Context), q) {
			continue
		}
		sb.WriteString(fmtEpisode(ep))
		count++
	}

	if count == 0 {
		return "No memories found.", nil
	}
	return sb.String(), nil
}

func execMemRecall(rawArgs json.RawMessage) (string, error) {
	dir, err := getMemoryPath()
	if err != nil {
		return "", err
	}

	var args struct {
		EntityID string `json:"entity_id"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.EntityID == "" {
		return "", fmt.Errorf("entity_id is required")
	}

	mu := memLock(dir)
	mu.Lock()
	store, err := loadMemoryData(dir)
	mu.Unlock()
	if err != nil {
		return "", err
	}

	e, ok := store.Entities[args.EntityID]
	if !ok {
		return fmt.Sprintf("Entity %q not found.", args.EntityID), nil
	}

	var sb strings.Builder
	sb.WriteString(fmtEntity(args.EntityID, e))

	// Linked episodes
	var linked []memoryEpisode
	for _, ep := range store.Episodes {
		if ep.Context == args.EntityID {
			linked = append(linked, ep)
		}
	}
	if len(linked) > 0 {
		sb.WriteString(fmt.Sprintf("\n--- %d episodes ---\n", len(linked)))
		for _, ep := range linked {
			sb.WriteString(fmtEpisode(ep))
		}
	}

	// Relations
	if len(e.Relations) > 0 {
		sb.WriteString("\n--- relations ---\n")
		for _, r := range e.Relations {
			name := r.Target
			if te, ok := store.Entities[r.Target]; ok && te.Name != "" {
				name = te.Name
			}
			sb.WriteString(fmt.Sprintf("  -[%s]-> %s\n", r.Rel, name))
		}
	}

	return sb.String(), nil
}

func execMemForget(rawArgs json.RawMessage) (string, error) {
	dir, err := getMemoryPath()
	if err != nil {
		return "", err
	}

	var args struct {
		EntityID  string `json:"entity_id"`
		EpisodeID string `json:"episode_id"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.EntityID == "" && args.EpisodeID == "" {
		return "", fmt.Errorf("provide entity_id or episode_id")
	}

	mu := memLock(dir)
	mu.Lock()
	defer mu.Unlock()

	store, err := loadMemoryData(dir)
	if err != nil {
		return "", err
	}

	var parts []string

	if args.EntityID != "" {
		if _, ok := store.Entities[args.EntityID]; !ok {
			return fmt.Sprintf("Entity %q not found.", args.EntityID), nil
		}
		delete(store.Entities, args.EntityID)
		// Remove linked episodes
		n := 0
		filtered := store.Episodes[:0]
		for _, ep := range store.Episodes {
			if ep.Context == args.EntityID {
				n++
			} else {
				filtered = append(filtered, ep)
			}
		}
		store.Episodes = filtered
		parts = append(parts, fmt.Sprintf("Deleted entity %q and %d linked episodes.", args.EntityID, n))
	}

	if args.EpisodeID != "" {
		found := false
		filtered := store.Episodes[:0]
		for _, ep := range store.Episodes {
			if ep.ID == args.EpisodeID {
				found = true
			} else {
				filtered = append(filtered, ep)
			}
		}
		if !found {
			if len(parts) == 0 {
				return fmt.Sprintf("Episode %q not found.", args.EpisodeID), nil
			}
		} else {
			store.Episodes = filtered
			parts = append(parts, fmt.Sprintf("Deleted episode %q.", args.EpisodeID))
		}
	}

	if err := saveMemoryData(dir, store); err != nil {
		return "", fmt.Errorf("save: %w", err)
	}
	return strings.Join(parts, "\n"), nil
}

// --- Helpers ---

func mergeStrings(existing, add []string) []string {
	set := map[string]bool{}
	for _, s := range existing {
		set[s] = true
	}
	for _, s := range add {
		if !set[s] {
			existing = append(existing, s)
			set[s] = true
		}
	}
	return existing
}

func memHasAllTags(have, want []string) bool {
	set := map[string]bool{}
	for _, t := range have {
		set[t] = true
	}
	for _, t := range want {
		if !set[t] {
			return false
		}
	}
	return true
}

func entityMatchesQuery(id string, e *memoryEntity, q string) bool {
	if strings.Contains(strings.ToLower(id), q) || strings.Contains(strings.ToLower(e.Name), q) {
		return true
	}
	for _, f := range e.Facts {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

func fmtEntity(id string, e *memoryEntity) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[%s] %s (type: %s, updated: %s)\n", id, e.Name, e.Type, e.Updated))
	for _, f := range e.Facts {
		sb.WriteString(fmt.Sprintf("  • %s\n", f))
	}
	if len(e.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("  tags: %s\n", strings.Join(e.Tags, ", ")))
	}
	return sb.String()
}

func fmtEpisode(ep memoryEpisode) string {
	ctx := ""
	if ep.Context != "" {
		ctx = fmt.Sprintf(" [%s]", ep.Context)
	}
	return fmt.Sprintf("  %s (%s)%s %s (id:%s)\n", ep.Time, ep.Type, ctx, ep.Summary, ep.ID)
}

// MemoryLookup searches persistent memory for the given query and returns
// a brief text summary. Returns "" if memory is not available or nothing found.
// Intended for Go-level injection into sub-agent inputs.
func MemoryLookup(query string) string {
	dir, err := getMemoryPath()
	if err != nil {
		return ""
	}
	mu := memLock(dir)
	mu.Lock()
	store, err := loadMemoryData(dir)
	mu.Unlock()
	if err != nil {
		return ""
	}

	q := strings.ToLower(query)
	var sb strings.Builder

	for id, e := range store.Entities {
		if entityMatchesQuery(id, e, q) {
			sb.WriteString(fmtEntity(id, e))
		}
	}

	// Recent episodes mentioning this query (last 10)
	count := 0
	for i := len(store.Episodes) - 1; i >= 0 && count < 10; i-- {
		ep := store.Episodes[i]
		if strings.Contains(strings.ToLower(ep.Summary), q) ||
			strings.Contains(strings.ToLower(ep.Context), q) {
			sb.WriteString(fmtEpisode(ep))
			count++
		}
	}

	return sb.String()
}

// --- Session-scoped temporary storage ---

var tempStore sync.Map // goroutineID → map[string]string

func getTempMap() map[string]string {
	gid := goroutineID()
	v, ok := tempStore.Load(gid)
	if !ok {
		m := make(map[string]string)
		tempStore.Store(gid, m)
		return m
	}
	return v.(map[string]string)
}

// ClearTempMemory removes all session-scoped temp data for the current goroutine.
func ClearTempMemory() {
	tempStore.Delete(goroutineID())
}

func execTempPut(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	json.Unmarshal(rawArgs, &args)
	if args.Key == "" {
		return "", fmt.Errorf("key is required")
	}

	m := getTempMap()
	m[args.Key] = args.Value
	return fmt.Sprintf("Stored: %s (%d bytes)", args.Key, len(args.Value)), nil
}

func execTempGet(rawArgs json.RawMessage) (string, error) {
	var args struct {
		Key string `json:"key"`
	}
	json.Unmarshal(rawArgs, &args)

	m := getTempMap()

	if args.Key != "" {
		v, ok := m[args.Key]
		if !ok {
			return fmt.Sprintf("Key %q not found.", args.Key), nil
		}
		return v, nil
	}

	// List all
	if len(m) == 0 {
		return "No temp data stored in this session.", nil
	}
	var sb strings.Builder
	for k, v := range m {
		preview := v
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		sb.WriteString(fmt.Sprintf("[%s] %s\n", k, preview))
	}
	return sb.String(), nil
}
