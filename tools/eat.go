package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
)

func init() {
	RegisterCommand(&Command{
		Name:       "eat",
		MCPServers: []string{"nutricalc"},
		Handler:    handleEat,
	})
}

// --- Data types ---

type eatItem struct {
	Name   string
	Weight float64 // grams (or count for шт), 0 if unspecified
	Unit   string  // "г", "шт", "мл", "" (default grams)
}

type catalogSearchResult struct {
	Results []catalogSearchMatch `json:"results"`
}

type catalogSearchMatch struct {
	ID        string         `json:"id"`
	ServingID string         `json:"servingId"`
	Name      string         `json:"name"`
	Detail    string         `json:"detail"`
	Serving   catalogServing `json:"serving"`
}

type catalogServing struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	Quantity float64 `json:"quantity"`
	Unit     string  `json:"unit"`
	Macros   macros  `json:"macros"`
}

type macros struct {
	Calories float64 `json:"calories"`
	Protein  float64 `json:"protein"`
	Carbs    float64 `json:"carbs"`
	Fats     float64 `json:"fats"`
}

type catalogItem struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Notes    string           `json:"notes"`
	Servings []catalogServing `json:"servings"`
}

type catalogFile struct {
	Catalog struct {
		Items map[string]catalogItem `json:"items"`
	} `json:"catalog"`
}

type dayStats struct {
	Eaten   macros `json:"eaten"`
	Targets struct {
		CalorieCeiling float64 `json:"calorieCeiling"`
		ProteinFloor   float64 `json:"proteinFloor"`
		CarbCeiling    float64 `json:"carbCeiling"`
	} `json:"targets"`
	Summary struct {
		CaloriesRemaining float64 `json:"caloriesRemaining"`
		ProteinRemaining  float64 `json:"proteinRemaining"`
	} `json:"summary"`
}

// --- Main handler ---

func handleEat(ctx *CommandContext) (string, error) {
	// Step 0: Resolve username
	username, err := resolveNutricalcUsername()
	if err != nil {
		return "", fmt.Errorf("username resolution: %w", err)
	}

	text := strings.TrimSpace(ctx.Text)
	today := time.Now().Format("2006-01-02")

	// Step 1: Determine mode
	switch {
	case len(ctx.Images) > 0:
		return handleEatImage(ctx, username, today)
	case text == "":
		return handleEatStats(username, today)
	default:
		return handleEatText(text, username, today)
	}
}

// --- Stats-only mode ---

func handleEatStats(username, date string) (string, error) {
	return fetchAndFormatDayStats(username, date)
}

// --- Text mode ---

func handleEatText(text, username, date string) (string, error) {
	items := parseEatItems(text)
	if len(items) == 0 {
		return "", fmt.Errorf("не удалось распознать продукты в: %s", text)
	}

	var results []string
	for _, item := range items {
		result, err := processEatItem(item, username, date)
		if err != nil {
			results = append(results, fmt.Sprintf("%s: ошибка — %v", item.Name, err))
			continue
		}
		results = append(results, result)
	}

	// Show daily stats after all items
	stats, err := fetchAndFormatDayStats(username, date)
	if err != nil {
		stats = fmt.Sprintf("(ошибка статистики: %v)", err)
	}

	return strings.Join(results, "\n") + "\n\n" + stats, nil
}

// --- Image mode ---

func handleEatImage(ctx *CommandContext, username, date string) (string, error) {
	if SubAgentImageFn == nil {
		return "", fmt.Errorf("image analysis not available")
	}

	systemPrompt := `You extract food items from images. Return a JSON array of objects:
[{"name": "food name", "weight_g": 100, "calories": 250, "protein": 20, "carbs": 30, "fats": 10}]
If it's a nutrition label, extract per-100g values. Use Russian names when possible.
Return ONLY the JSON array, no other text.`

	resp, err := SubAgentImageFn(systemPrompt, "Extract food items from this image as JSON array.", ctx.Images)
	if err != nil {
		return "", fmt.Errorf("image analysis: %w", err)
	}

	raw, err := extractJSON(resp)
	if err != nil {
		return "", fmt.Errorf("parse image response: %w", err)
	}

	var extracted []struct {
		Name     string  `json:"name"`
		WeightG  float64 `json:"weight_g"`
		Calories float64 `json:"calories"`
		Protein  float64 `json:"protein"`
		Carbs    float64 `json:"carbs"`
		Fats     float64 `json:"fats"`
	}
	if err := json.Unmarshal(raw, &extracted); err != nil {
		return "", fmt.Errorf("parse extracted items: %w", err)
	}

	var results []string
	for _, ex := range extracted {
		item := eatItem{Name: ex.Name, Weight: ex.WeightG, Unit: "г"}
		if item.Weight == 0 {
			item.Weight = 100
		}
		result, err := processEatItem(item, username, date)
		if err != nil {
			results = append(results, fmt.Sprintf("%s: ошибка — %v", ex.Name, err))
			continue
		}
		results = append(results, result)
	}

	stats, err := fetchAndFormatDayStats(username, date)
	if err != nil {
		stats = fmt.Sprintf("(ошибка статистики: %v)", err)
	}

	return strings.Join(results, "\n") + "\n\n" + stats, nil
}

// --- Process a single eat item ---

func processEatItem(item eatItem, username, date string) (string, error) {
	// Search catalog
	match, err := findCatalogMatch(item.Name)
	if err != nil {
		return "", err
	}

	if match == nil {
		// No match — offer to add or skip
		if AskAvailable() {
			answer, askErr := GetPrompter().Ask(UserQuestion{
				Question: fmt.Sprintf("'%s' не найден в каталоге.", item.Name),
				Options: []UserOption{
					{Label: "Добавить в каталог"},
					{Label: "Пропустить"},
				},
			})
			if askErr != nil {
				return "", askErr
			}
			if answer == "Пропустить" {
				return fmt.Sprintf("%s: пропущен", item.Name), nil
			}
			// Estimate macros via AI and add to catalog
			return handleAddToCatalog(item, username, date)
		}
		return "", fmt.Errorf("'%s' не найден в каталоге", item.Name)
	}

	// Find best serving and calculate quantity
	serving := bestServing(match.Servings, item.Unit)
	quantity := calculateQuantity(item, serving)

	// Calculate actual macros
	actualMacros := macros{
		Calories: math.Round(serving.Macros.Calories*quantity*10) / 10,
		Protein:  math.Round(serving.Macros.Protein*quantity*10) / 10,
		Carbs:    math.Round(serving.Macros.Carbs*quantity*10) / 10,
		Fats:     math.Round(serving.Macros.Fats*quantity*10) / 10,
	}

	// Build diary_add_meal args
	mealArgs := map[string]any{
		"user":       username,
		"date":       date,
		"label":      item.Name,
		"calories":   actualMacros.Calories,
		"protein":    actualMacros.Protein,
		"carbs":      actualMacros.Carbs,
		"fats":       actualMacros.Fats,
		"item_id":    match.ID,
		"serving_id": serving.ID,
		"quantity":   math.Round(quantity*1000) / 1000,
	}

	_, err = mcpCall("nutricalc__diary_add_meal", mealArgs)
	if err != nil {
		return "", fmt.Errorf("diary_add_meal: %w", err)
	}

	weightStr := ""
	if item.Weight > 0 {
		if item.Unit == "шт" || item.Unit == "pcs" {
			weightStr = fmt.Sprintf(" %.0f%s", item.Weight, item.Unit)
		} else {
			weightStr = fmt.Sprintf(" %.0fг", item.Weight)
		}
	}

	return fmt.Sprintf("+ %s%s (%.0f ккал, %.1fб, %.1fу, %.1fж)",
		match.Name, weightStr, actualMacros.Calories, actualMacros.Protein, actualMacros.Carbs, actualMacros.Fats), nil
}

// --- Catalog search cascade ---

func findCatalogMatch(name string) (*catalogItem, error) {
	// Load full catalog upfront — we need complete serving data (with quantity/unit)
	// that catalog_search doesn't return.
	catalog := loadFullCatalog()

	// 1. Direct catalog_search
	searchResult, err := mcpCall("nutricalc__catalog_search", map[string]any{
		"query": name,
		"limit": 5,
	})
	if err == nil {
		var sr catalogSearchResult
		if json.Unmarshal([]byte(searchResult), &sr) == nil && len(sr.Results) > 0 {
			picked := pickSearchResult(sr.Results)
			if picked != nil {
				return enrichFromCatalog(picked, catalog), nil
			}
		}
	}

	// 2. Try individual words (≥3 chars)
	words := significantWords(name)
	for _, word := range words {
		searchResult, err = mcpCall("nutricalc__catalog_search", map[string]any{
			"query": word,
			"limit": 5,
		})
		if err == nil {
			var sr catalogSearchResult
			if json.Unmarshal([]byte(searchResult), &sr) == nil && len(sr.Results) > 0 {
				picked := pickSearchResult(sr.Results)
				if picked != nil {
					return enrichFromCatalog(picked, catalog), nil
				}
			}
		}
	}

	// 3. Full catalog fallback — substring match
	if catalog == nil {
		return nil, nil
	}

	nameLower := strings.ToLower(name)
	var matches []catalogItem
	for _, item := range catalog {
		if strings.Contains(strings.ToLower(item.Name), nameLower) ||
			strings.Contains(strings.ToLower(item.Notes), nameLower) {
			matches = append(matches, item)
		}
	}
	// Also try individual words
	if len(matches) == 0 {
		for _, word := range words {
			wordLower := strings.ToLower(word)
			for _, item := range catalog {
				if strings.Contains(strings.ToLower(item.Name), wordLower) ||
					strings.Contains(strings.ToLower(item.Notes), wordLower) {
					matches = append(matches, item)
				}
			}
			if len(matches) > 0 {
				break
			}
		}
	}

	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) == 1 {
		return &matches[0], nil
	}

	// Multiple matches from full catalog — ask user
	if AskAvailable() {
		return askUserToPickCatalogItem(matches)
	}
	return &matches[0], nil
}

// loadFullCatalog fetches the complete catalog file. Returns item map or nil.
func loadFullCatalog() map[string]catalogItem {
	fileResult, err := mcpCall("nutricalc__catalog_get_file", map[string]any{})
	if err != nil {
		return nil
	}
	var cf catalogFile
	if json.Unmarshal([]byte(fileResult), &cf) != nil {
		return nil
	}
	return cf.Catalog.Items
}

// enrichFromCatalog replaces a search-result item with full catalog data
// (complete servings with quantity/unit). Falls back to the original if not found.
func enrichFromCatalog(item *catalogItem, catalog map[string]catalogItem) *catalogItem {
	if catalog == nil || item.ID == "" {
		return item
	}
	if full, ok := catalog[item.ID]; ok {
		return &full
	}
	return item
}

// pickSearchResult handles single vs. multiple search results.
// Returns nil if user chose to skip.
func pickSearchResult(results []catalogSearchMatch) *catalogItem {
	if len(results) == 1 {
		return searchResultToItem(results[0])
	}
	if AskAvailable() {
		item, _ := askUserToPickResult(results)
		return item
	}
	return searchResultToItem(results[0])
}

func askUserToPickResult(results []catalogSearchMatch) (*catalogItem, error) {
	var options []UserOption
	for _, r := range results {
		label := r.Name
		if r.Serving.Macros.Calories > 0 {
			label += fmt.Sprintf(" (%.0f ккал/%s)", r.Serving.Macros.Calories, r.Serving.Label)
		}
		options = append(options, UserOption{Label: label})
	}
	options = append(options, UserOption{Label: "Пропустить"})

	answer, err := GetPrompter().Ask(UserQuestion{
		Question: "Выберите продукт:",
		Options:  options,
	})
	if err != nil {
		return nil, err
	}
	if answer == "Пропустить" {
		return nil, nil
	}

	// Match answer to result
	for i, opt := range options {
		if i < len(results) && opt.Label == answer {
			return searchResultToItem(results[i]), nil
		}
	}
	// Fallback: first result
	return searchResultToItem(results[0]), nil
}

func askUserToPickCatalogItem(items []catalogItem) (*catalogItem, error) {
	var options []UserOption
	limit := len(items)
	if limit > 8 {
		limit = 8
	}
	for _, item := range items[:limit] {
		label := item.Name
		if len(item.Servings) > 0 {
			s := item.Servings[0]
			label += fmt.Sprintf(" (%.0f ккал/%s)", s.Macros.Calories, s.Label)
		}
		options = append(options, UserOption{Label: label})
	}
	options = append(options, UserOption{Label: "Пропустить"})

	answer, err := GetPrompter().Ask(UserQuestion{
		Question: "Выберите продукт:",
		Options:  options,
	})
	if err != nil {
		return nil, err
	}
	if answer == "Пропустить" {
		return nil, nil
	}

	for i, opt := range options {
		if i < limit && opt.Label == answer {
			return &items[i], nil
		}
	}
	return &items[0], nil
}

func searchResultToItem(r catalogSearchMatch) *catalogItem {
	return &catalogItem{
		ID:   r.ID,
		Name: r.Name,
		Servings: []catalogServing{
			r.Serving,
		},
	}
}

// --- Serving selection and quantity calculation ---

func bestServing(servings []catalogServing, userUnit string) catalogServing {
	if len(servings) == 0 {
		return catalogServing{Quantity: 100, Macros: macros{}}
	}

	// If user specified шт, prefer шт serving
	if userUnit == "шт" || userUnit == "pcs" {
		for _, s := range servings {
			if s.Label == "шт" || s.Label == "pcs" || s.Label == "порция" {
				return s
			}
		}
	}

	// Prefer 100g serving
	for _, s := range servings {
		lbl := strings.ToLower(s.Label)
		if lbl == "100g" || lbl == "100 g" || lbl == "100г" || lbl == "100 г" {
			return s
		}
	}

	// Fallback: any gram-based serving
	for _, s := range servings {
		u := strings.ToLower(s.Unit)
		if u == "г" || u == "g" {
			return s
		}
	}

	return servings[0]
}

func calculateQuantity(item eatItem, serving catalogServing) float64 {
	if item.Weight == 0 {
		return 1 // default: 1 serving
	}
	if item.Unit == "шт" || item.Unit == "pcs" {
		return item.Weight // direct piece count
	}
	if serving.Quantity <= 0 {
		return 1
	}
	return item.Weight / serving.Quantity
}

// --- Add to catalog (AI-estimated macros) ---

func handleAddToCatalog(item eatItem, username, date string) (string, error) {
	// Ask user for macros or use AI estimate
	var cal, prot, carb, fat float64

	if SubAgentFn != nil {
		estimate, err := SubAgentFn(
			"You are a nutrition expert. Estimate macros per 100g for the given food. Return ONLY JSON: {\"calories\":N,\"protein\":N,\"carbs\":N,\"fats\":N}",
			fmt.Sprintf("Estimate macros per 100g for: %s", item.Name),
		)
		if err == nil {
			raw, _ := extractJSON(estimate)
			var m macros
			if json.Unmarshal(raw, &m) == nil {
				cal, prot, carb, fat = m.Calories, m.Protein, m.Carbs, m.Fats
			}
		}
	}

	if cal == 0 {
		// Fallback: ask user
		if !AskAvailable() {
			return "", fmt.Errorf("cannot estimate macros for '%s'", item.Name)
		}
		answer, err := GetPrompter().Ask(UserQuestion{
			Question: fmt.Sprintf("Введите КБЖУ на 100г для '%s' (через пробел: калории белки углеводы жиры):", item.Name),
		})
		if err != nil {
			return "", err
		}
		parts := strings.Fields(answer)
		if len(parts) >= 4 {
			fmt.Sscanf(parts[0], "%f", &cal)
			fmt.Sscanf(parts[1], "%f", &prot)
			fmt.Sscanf(parts[2], "%f", &carb)
			fmt.Sscanf(parts[3], "%f", &fat)
		}
	}

	// Add to catalog
	addArgs := map[string]any{
		"name":     item.Name,
		"calories": cal,
		"protein":  prot,
		"carbs":    carb,
		"fats":     fat,
	}
	_, err := mcpCall("nutricalc__catalog_add_product", addArgs)
	if err != nil {
		return "", fmt.Errorf("catalog_add_product: %w", err)
	}

	// Now search for the newly added item and log it
	return processEatItem(item, username, date)
}

// --- Username resolution ---

func resolveNutricalcUsername() (string, error) {
	// Try memory_search
	if tool, ok := Get("memory_search"); ok {
		args, _ := json.Marshal(map[string]string{"query": "nutricalc username"})
		result, err := tool.Execute(args)
		if err == nil && result != "" && result != "No memories found." {
			// Parse username from memory result
			if name := extractUsernameFromMemory(result); name != "" {
				return name, nil
			}
		}
	}

	// Ask user if prompter available
	if AskAvailable() {
		answer, err := GetPrompter().Ask(UserQuestion{
			Question: "Какое имя пользователя в nutricalc?",
		})
		if err != nil {
			return "", err
		}
		username := strings.TrimSpace(answer)
		if username == "" {
			return "", fmt.Errorf("empty username")
		}

		// Save to memory
		if tool, ok := Get("memory_store"); ok {
			args, _ := json.Marshal(map[string]any{
				"entity_id": "pref:nutricalc_username",
				"type":      "preference",
				"name":      "nutricalc username",
				"facts":     fmt.Sprintf(`["%s"]`, username),
			})
			tool.Execute(args)
		}

		return username, nil
	}

	return "", fmt.Errorf("nutricalc username not found in memory and no way to ask user")
}

func extractUsernameFromMemory(memResult string) string {
	// Memory format: [pref:nutricalc_username] nutricalc username (type: preference, updated: ...)
	//   • username_value
	lines := strings.Split(memResult, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "•") || strings.HasPrefix(line, "- ") {
			val := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "•"), "- "))
			if val != "" {
				return val
			}
		}
	}
	// Also check for "User answered: X" pattern
	for _, line := range lines {
		if strings.HasPrefix(line, "User answered:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "User answered:"))
		}
	}
	return ""
}

// --- Day stats ---

func fetchAndFormatDayStats(username, date string) (string, error) {
	result, err := mcpCall("nutricalc__diary_day_stats", map[string]any{
		"user": username,
		"date": date,
	})
	if err != nil {
		return "", fmt.Errorf("diary_day_stats: %w", err)
	}
	return formatDayStats(result), nil
}

func formatDayStats(statsJSON string) string {
	var ds dayStats
	if err := json.Unmarshal([]byte(statsJSON), &ds); err != nil {
		return statsJSON // fallback: raw
	}

	return fmt.Sprintf("Итого: %.0f/%.0f ккал | Б: %.0f/%.0fг | У: %.0f/%.0fг | Ж: %.0fг\nОсталось: %.0f ккал, %.0fг белка",
		ds.Eaten.Calories, ds.Targets.CalorieCeiling,
		ds.Eaten.Protein, ds.Targets.ProteinFloor,
		ds.Eaten.Carbs, ds.Targets.CarbCeiling,
		ds.Eaten.Fats,
		ds.Summary.CaloriesRemaining, ds.Summary.ProteinRemaining,
	)
}

// --- Parse helpers ---

var eatWeightRe = regexp.MustCompile(`(\d+[.,]?\d*)\s*(г|g|гр|мл|ml|шт|pcs)\s*$`)

func parseEatItems(text string) []eatItem {
	// Split by comma and newline
	var segments []string
	for _, line := range strings.Split(text, "\n") {
		for _, seg := range strings.Split(line, ",") {
			seg = strings.TrimSpace(seg)
			if seg != "" {
				segments = append(segments, seg)
			}
		}
	}

	var items []eatItem
	for _, seg := range segments {
		item := parseOneItem(seg)
		if item.Name != "" {
			items = append(items, item)
		}
	}
	return items
}

func parseOneItem(seg string) eatItem {
	m := eatWeightRe.FindStringSubmatchIndex(seg)
	if m == nil {
		// No weight — just a name
		return eatItem{Name: strings.TrimSpace(seg)}
	}

	name := strings.TrimSpace(seg[:m[0]])
	weightStr := seg[m[2]:m[3]]
	unit := seg[m[4]:m[5]]

	weightStr = strings.Replace(weightStr, ",", ".", 1)
	var weight float64
	fmt.Sscanf(weightStr, "%f", &weight)

	// Normalize unit
	switch unit {
	case "гр", "g":
		unit = "г"
	case "pcs":
		unit = "шт"
	}

	return eatItem{Name: name, Weight: weight, Unit: unit}
}

func significantWords(name string) []string {
	words := strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	var result []string
	for _, w := range words {
		if len([]rune(w)) >= 3 {
			result = append(result, w)
		}
	}
	return result
}

// --- JSON extraction from AI response ---

func extractJSON(text string) (json.RawMessage, error) {
	// Strip ```json fences
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		if idx := strings.Index(text, "\n"); idx >= 0 {
			text = text[idx+1:]
		}
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = text[:idx]
		}
		text = strings.TrimSpace(text)
	}

	// Find first [ or {
	start := -1
	var closer byte
	for i := 0; i < len(text); i++ {
		if text[i] == '[' {
			start = i
			closer = ']'
			break
		}
		if text[i] == '{' {
			start = i
			closer = '}'
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("no JSON found in response")
	}

	// Find matching closer from end
	end := strings.LastIndexByte(text, closer)
	if end <= start {
		return nil, fmt.Errorf("no matching %c found", closer)
	}

	raw := json.RawMessage(text[start : end+1])
	// Validate
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON extracted")
	}
	return raw, nil
}

// --- MCP helper ---

func mcpCall(tool string, args map[string]any) (string, error) {
	if MCPCallFn == nil {
		return "", fmt.Errorf("MCP not configured")
	}
	data, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return MCPCallFn(tool, data)
}
