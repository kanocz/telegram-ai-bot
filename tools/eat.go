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

// nutriTimestamp returns the current local time as an RFC3339 string with
// timezone offset. The nutricalc server requires RFC3339 and preserves the
// offset; without it the server defaults to "now" in UTC, which shows meals
// shifted by the local UTC offset (e.g. -2h in CEST).
func nutriTimestamp() string {
	return time.Now().Format(time.RFC3339)
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
	// Check if this is a catalog-add with inline macros
	if ca := parseCatalogAdd(text); ca != nil {
		return handleCatalogAdd(ca, username, date)
	}

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

// imageAnalysisResult is the structured response from vision AI.
type imageAnalysisResult struct {
	Type    string                  `json:"type"` // "food" or "label"
	Items   []imageAnalysisFoodItem `json:"items,omitempty"`
	Name    string                  `json:"name,omitempty"`
	Per100g macros                  `json:"per_100g,omitempty"`
}

type imageAnalysisFoodItem struct {
	Name    string  `json:"name"`
	WeightG float64 `json:"weight_g"`
}

func handleEatImage(ctx *CommandContext, username, date string) (string, error) {
	if SubAgentImageFn == nil {
		return "", fmt.Errorf("image analysis not available")
	}

	// Call AI with image only — caption text is parsed by Go code, not sent to AI.
	systemPrompt := `You analyze food images. Classify the image:
1. "label" — nutrition label, food package with nutrition facts
2. "food" — photo of prepared food, dish, meal, raw ingredients

For type "label": extract product name (if visible) and nutritional values per 100g.
Return: {"type": "label", "name": "product name", "per_100g": {"calories": N, "protein": N, "carbs": N, "fats": N}}

For type "food": identify each food item and estimate weight in grams.
Return: {"type": "food", "items": [{"name": "food name", "weight_g": N}]}

Rules: use Russian names when possible. For labels: ALWAYS normalize to per 100g.
For food: estimate realistic portion weights. Return ONLY JSON.`

	thinkOn := true
	resp, err := SubAgentImageFn(systemPrompt, "Analyze this image.", ctx.Images, &thinkOn)
	if err != nil {
		return "", fmt.Errorf("image analysis: %w", err)
	}

	raw, err := extractJSON(resp)
	if err != nil {
		return "", fmt.Errorf("parse image response: %w", err)
	}

	var analysis imageAnalysisResult
	if err := json.Unmarshal(raw, &analysis); err != nil {
		return "", fmt.Errorf("parse analysis: %w", err)
	}

	switch analysis.Type {
	case "label":
		return handleEatLabel(analysis, ctx.Text, username, date)
	default:
		return handleEatFood(analysis.Items, username, date)
	}
}

// handleEatFood processes a food photo: each recognized item goes through the normal catalog search flow.
func handleEatFood(items []imageAnalysisFoodItem, username, date string) (string, error) {
	var results []string
	for _, ex := range items {
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

// handleEatLabel processes a nutrition label photo.
// Caption text is parsed for product name and/or weight — it is NOT sent to the AI.
func handleEatLabel(analysis imageAnalysisResult, captionText string, username, date string) (string, error) {
	per100g := analysis.Per100g
	labelName := analysis.Name

	// Parse caption for name and/or weight.
	caption := strings.TrimSpace(captionText)
	var itemName string
	var weight float64
	var weightUnit string

	if caption != "" {
		parsed := parseOneItem(caption)
		itemName = parsed.Name
		weight = parsed.Weight
		weightUnit = parsed.Unit
	}

	// Determine final product name: caption name > label name > ask user.
	finalName := itemName
	if finalName == "" {
		finalName = labelName
	}
	if finalName == "" && AskAvailable() {
		answer, err := GetPrompter().Ask(UserQuestion{
			Question: "Название продукта не распознано. Введите название:",
		})
		if err != nil {
			return "", err
		}
		finalName = strings.TrimSpace(strings.TrimPrefix(answer, "User answered: "))
	}
	if finalName == "" {
		return "", fmt.Errorf("не удалось определить название продукта")
	}

	if weightUnit == "" && weight > 0 {
		weightUnit = "г"
	}

	if weight > 0 {
		return handleLabelWithWeight(finalName, per100g, weight, weightUnit, username, date)
	}
	return handleLabelNoWeight(finalName, per100g, username, date)
}

// handleLabelWithWeight handles a label photo when caption includes weight (e.g. "творог 20г").
func handleLabelWithWeight(name string, per100g macros, weight float64, weightUnit string, username, date string) (string, error) {
	ratio := weight / 100
	diaryMacros := macros{
		Calories: math.Round(per100g.Calories*ratio*10) / 10,
		Protein:  math.Round(per100g.Protein*ratio*10) / 10,
		Carbs:    math.Round(per100g.Carbs*ratio*10) / 10,
		Fats:     math.Round(per100g.Fats*ratio*10) / 10,
	}

	if !AskAvailable() {
		return "", fmt.Errorf("need interactive mode for label processing")
	}

	answer, err := GetPrompter().Ask(UserQuestion{
		Question: fmt.Sprintf("%s %.0f%s (%.0f ккал)\nна 100г: %.0f ккал, %.1fб, %.1fу, %.1fж",
			name, weight, weightUnit, diaryMacros.Calories,
			per100g.Calories, per100g.Protein, per100g.Carbs, per100g.Fats),
		Options: []UserOption{
			{Label: "Каталог + дневник"},
			{Label: "Только дневник"},
			{Label: "Только каталог"},
			{Label: "Отмена"},
		},
	})
	if err != nil {
		return "", err
	}
	if answer == "Отмена" {
		return "Отменено", nil
	}

	var parts []string

	if answer != "Только дневник" {
		if err := catalogAddProduct(name, per100g, "AI", "100g", 100, "г"); err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("+ В каталог: %s (%.0f ккал/100г)", name, per100g.Calories))
	}

	if answer != "Только каталог" {
		mealArgs := map[string]any{
			"user":      username,
			"date":      date,
			"label":     name,
			"calories":  diaryMacros.Calories,
			"protein":   diaryMacros.Protein,
			"carbs":     diaryMacros.Carbs,
			"fats":      diaryMacros.Fats,
			"is_custom": true,
			"timestamp": nutriTimestamp(),
		}
		_, err := mcpCall("nutricalc__diary_add_meal", mealArgs)
		if err != nil {
			return "", fmt.Errorf("diary_add_meal: %w", err)
		}
		parts = append(parts, fmt.Sprintf("+ В дневник: %s %.0f%s (%.0f ккал, %.1fб, %.1fу, %.1fж)",
			name, weight, weightUnit, diaryMacros.Calories, diaryMacros.Protein, diaryMacros.Carbs, diaryMacros.Fats))

		stats, err := fetchAndFormatDayStats(username, date)
		if err == nil {
			parts = append(parts, "\n"+stats)
		}
	}

	return strings.Join(parts, "\n"), nil
}

// handleLabelNoWeight handles a label photo when no weight is specified in caption.
func handleLabelNoWeight(name string, per100g macros, username, date string) (string, error) {
	if !AskAvailable() {
		return fmt.Sprintf("Этикетка: %s\n%.0f ккал, %.1fб, %.1fу, %.1fж на 100г",
			name, per100g.Calories, per100g.Protein, per100g.Carbs, per100g.Fats), nil
	}

	answer, err := GetPrompter().Ask(UserQuestion{
		Question: fmt.Sprintf("%s — на 100г:\n%.0f ккал, %.1fб, %.1fу, %.1fж",
			name, per100g.Calories, per100g.Protein, per100g.Carbs, per100g.Fats),
		Options: []UserOption{
			{Label: "В каталог"},
			{Label: "В каталог + дневник"},
			{Label: "Отмена"},
		},
	})
	if err != nil {
		return "", err
	}
	if answer == "Отмена" {
		return "Отменено", nil
	}

	if err := catalogAddProduct(name, per100g, "AI", "100g", 100, "г"); err != nil {
		return "", err
	}

	parts := []string{fmt.Sprintf("+ В каталог: %s (%.0f ккал/100г)", name, per100g.Calories)}

	if answer == "В каталог + дневник" {
		weightAnswer, err := GetPrompter().Ask(UserQuestion{
			Question: "Введите вес в граммах:",
		})
		if err != nil {
			return strings.Join(parts, "\n"), nil
		}
		weightAnswer = strings.Replace(weightAnswer, ",", ".", 1)
		weightAnswer = strings.TrimPrefix(weightAnswer, "User answered: ")
		var w float64
		fmt.Sscanf(strings.TrimSpace(weightAnswer), "%f", &w)
		if w <= 0 {
			w = 100
		}

		ratio := w / 100
		diaryMacros := macros{
			Calories: math.Round(per100g.Calories*ratio*10) / 10,
			Protein:  math.Round(per100g.Protein*ratio*10) / 10,
			Carbs:    math.Round(per100g.Carbs*ratio*10) / 10,
			Fats:     math.Round(per100g.Fats*ratio*10) / 10,
		}

		mealArgs := map[string]any{
			"user":      username,
			"date":      date,
			"label":     name,
			"calories":  diaryMacros.Calories,
			"protein":   diaryMacros.Protein,
			"carbs":     diaryMacros.Carbs,
			"fats":      diaryMacros.Fats,
			"is_custom": true,
			"timestamp": nutriTimestamp(),
		}
		_, err = mcpCall("nutricalc__diary_add_meal", mealArgs)
		if err != nil {
			return "", fmt.Errorf("diary_add_meal: %w", err)
		}
		parts = append(parts, fmt.Sprintf("+ В дневник: %s %.0fг (%.0f ккал, %.1fб, %.1fу, %.1fж)",
			name, w, diaryMacros.Calories, diaryMacros.Protein, diaryMacros.Carbs, diaryMacros.Fats))

		stats, err := fetchAndFormatDayStats(username, date)
		if err == nil {
			parts = append(parts, "\n"+stats)
		}
	}

	return strings.Join(parts, "\n"), nil
}

// --- Process a single eat item ---

func processEatItem(item eatItem, username, date string) (string, error) {
	// Search catalog
	match, err := findCatalogMatch(item.Name, item.Unit)
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

	// If no weight specified, ask user to pick a serving or enter weight
	if item.Weight == 0 && len(match.Servings) > 0 && AskAvailable() {
		chosen, qty, askErr := askServingOrWeight(match)
		if askErr != nil {
			return "", askErr
		}
		if chosen != nil {
			// User picked a serving — use it directly
			serving := *chosen
			quantity := qty
			actualMacros := macros{
				Calories: math.Round(serving.Macros.Calories*quantity*10) / 10,
				Protein:  math.Round(serving.Macros.Protein*quantity*10) / 10,
				Carbs:    math.Round(serving.Macros.Carbs*quantity*10) / 10,
				Fats:     math.Round(serving.Macros.Fats*quantity*10) / 10,
			}
			return addAndReport(match, item.Name, serving, quantity, actualMacros, username, date)
		}
		// qty holds the weight in grams the user typed — update item
		item.Weight = qty
		item.Unit = "г"
	}

	// Find best serving and calculate quantity
	serving := bestServing(match.Servings, item.Unit)
	quantity := calculateQuantity(item, serving)

	actualMacros := macros{
		Calories: math.Round(serving.Macros.Calories*quantity*10) / 10,
		Protein:  math.Round(serving.Macros.Protein*quantity*10) / 10,
		Carbs:    math.Round(serving.Macros.Carbs*quantity*10) / 10,
		Fats:     math.Round(serving.Macros.Fats*quantity*10) / 10,
	}

	return addAndReport(match, item.Name, serving, quantity, actualMacros, username, date)
}

// addAndReport calls diary_add_meal and returns a formatted confirmation line.
func addAndReport(match *catalogItem, label string, serving catalogServing, quantity float64, m macros, username, date string) (string, error) {
	mealArgs := map[string]any{
		"user":       username,
		"date":       date,
		"label":      label,
		"calories":   m.Calories,
		"protein":    m.Protein,
		"carbs":      m.Carbs,
		"fats":       m.Fats,
		"item_id":    match.ID,
		"serving_id": serving.ID,
		"quantity":   math.Round(quantity*1000) / 1000,
		"timestamp":  nutriTimestamp(),
	}

	_, err := mcpCall("nutricalc__diary_add_meal", mealArgs)
	if err != nil {
		return "", fmt.Errorf("diary_add_meal: %w", err)
	}

	// Format weight/qty for display
	weightStr := ""
	if quantity != 1 || serving.Quantity > 0 {
		grams := quantity * serving.Quantity
		if grams > 0 {
			weightStr = fmt.Sprintf(" %.0fг", grams)
		}
	}

	return fmt.Sprintf("+ %s%s (%.0f ккал, %.1fб, %.1fу, %.1fж)",
		match.Name, weightStr, m.Calories, m.Protein, m.Carbs, m.Fats), nil
}

// askServingOrWeight presents available servings as buttons plus "Указать вес".
// Returns (serving, quantity, err). If serving is nil, quantity holds the weight in grams.
func askServingOrWeight(match *catalogItem) (*catalogServing, float64, error) {
	var options []UserOption
	for _, s := range match.Servings {
		label := s.Label
		if s.Macros.Calories > 0 {
			label += fmt.Sprintf(" (%.0f ккал)", s.Macros.Calories)
		}
		options = append(options, UserOption{Label: label})
	}
	options = append(options, UserOption{Label: "Указать вес"})

	answer, err := GetPrompter().Ask(UserQuestion{
		Question: fmt.Sprintf("%s — сколько?", match.Name),
		Options:  options,
	})
	if err != nil {
		return nil, 0, err
	}

	if answer == "Указать вес" {
		weightAnswer, err := GetPrompter().Ask(UserQuestion{
			Question: "Введите вес в граммах:",
		})
		if err != nil {
			return nil, 0, err
		}
		var w float64
		weightAnswer = strings.Replace(weightAnswer, ",", ".", 1)
		// Strip "User answered: " prefix if present
		weightAnswer = strings.TrimPrefix(weightAnswer, "User answered: ")
		fmt.Sscanf(strings.TrimSpace(weightAnswer), "%f", &w)
		if w <= 0 {
			w = 100
		}
		return nil, w, nil
	}

	// Match answer to a serving
	for i, opt := range options {
		if i < len(match.Servings) && opt.Label == answer {
			return &match.Servings[i], 1, nil
		}
	}
	// Fallback: first serving
	return &match.Servings[0], 1, nil
}

// --- Catalog search cascade ---

func findCatalogMatch(name, userUnit string) (*catalogItem, error) {
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
			picked := pickSearchResult(sr.Results, userUnit)
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
				picked := pickSearchResult(sr.Results, userUnit)
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

// pickSearchResult deduplicates results by item ID (same product, different
// servings) and asks only when there are truly different products.
// When the user specified a unit (г, мл, шт), auto-selects the matching
// serving variant instead of prompting.
func pickSearchResult(results []catalogSearchMatch, userUnit string) *catalogItem {
	// Group results by item ID
	type group struct {
		first   catalogSearchMatch
		results []catalogSearchMatch
	}
	groups := map[string]*group{}
	var order []string
	for _, r := range results {
		g, ok := groups[r.ID]
		if !ok {
			g = &group{first: r}
			groups[r.ID] = g
			order = append(order, r.ID)
		}
		g.results = append(g.results, r)
	}

	// For groups with multiple servings of the same product,
	// auto-select based on user's unit.
	var unique []catalogSearchMatch
	for _, id := range order {
		g := groups[id]
		if len(g.results) == 1 {
			unique = append(unique, g.first)
			continue
		}
		// Multiple servings for same product — pick best match for user's unit
		best := autoSelectServing(g.results, userUnit)
		unique = append(unique, best)
	}

	if len(unique) == 1 {
		return searchResultToItem(unique[0])
	}
	if AskAvailable() {
		item, _ := askUserToPickResult(unique)
		return item
	}
	return searchResultToItem(unique[0])
}

// autoSelectServing picks the best serving variant from same-product results
// based on the user's requested unit.
func autoSelectServing(results []catalogSearchMatch, userUnit string) catalogSearchMatch {
	if isWeightUnit(userUnit) {
		// User wants grams/ml — prefer 100g/100ml serving
		for _, r := range results {
			lbl := strings.ToLower(r.Serving.Label)
			if lbl == "100g" || lbl == "100 g" || lbl == "100г" || lbl == "100 г" ||
				lbl == "100ml" || lbl == "100 ml" || lbl == "100мл" || lbl == "100 мл" {
				return r
			}
		}
	} else if isPieceUnit(userUnit) {
		// User wants pieces/portions — prefer шт/порция serving
		for _, r := range results {
			lbl := strings.ToLower(r.Serving.Label)
			if lbl == "шт" || lbl == "pcs" || lbl == "порция" || lbl == "portion" {
				return r
			}
		}
	}
	return results[0]
}

func isWeightUnit(unit string) bool {
	switch unit {
	case "г", "g", "гр", "мл", "ml", "":
		return true
	}
	return false
}

func isPieceUnit(unit string) bool {
	switch unit {
	case "шт", "pcs", "порция":
		return true
	}
	return false
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
	m, err := estimateMacrosWithConfirm(item.Name)
	if err != nil {
		return "", err
	}

	// Add to catalog (macros are per 100g)
	if err := catalogAddProduct(item.Name, m, "AI", "100g", 100, "г"); err != nil {
		return "", err
	}

	// Now search for the newly added item and log it
	return processEatItem(item, username, date)
}

// handleCatalogAdd processes a multi-line catalog-add input.
// If weight is specified, asks whether macros are per-serving or per-100g,
// then offers to add to catalog, diary, or both.
func handleCatalogAdd(ca *catalogAddInput, username, date string) (string, error) {
	m := ca.Macros

	if ca.Weight > 0 {
		// Macros + weight: ask if values are for this weight or per 100g
		if !AskAvailable() {
			return "", fmt.Errorf("need to clarify: macros per %.0f%s or per 100г", ca.Weight, ca.WeightUnit)
		}

		answer, err := GetPrompter().Ask(UserQuestion{
			Question: fmt.Sprintf("%s — КБЖУ (%.0f/%.1f/%.1f/%.1f) это на:",
				ca.Name, m.Calories, m.Protein, m.Carbs, m.Fats),
			Options: []UserOption{
				{Label: fmt.Sprintf("%.0f%s (порцию)", ca.Weight, ca.WeightUnit)},
				{Label: "100г"},
			},
		})
		if err != nil {
			return "", err
		}

		perServing := answer != "100г"

		var catalogMacros macros // per 100g for catalog
		var diaryMacros macros   // actual for this weight
		var servingLabel string

		if perServing {
			// User says macros are for ca.Weight grams (a serving)
			// Catalog: store as "шт" serving with these exact macros
			catalogMacros = m
			diaryMacros = m
			servingLabel = "шт"
		} else {
			// User says macros are per 100g
			catalogMacros = m
			ratio := ca.Weight / 100
			diaryMacros = macros{
				Calories: math.Round(m.Calories*ratio*10) / 10,
				Protein:  math.Round(m.Protein*ratio*10) / 10,
				Carbs:    math.Round(m.Carbs*ratio*10) / 10,
				Fats:     math.Round(m.Fats*ratio*10) / 10,
			}
		}

		// Ask: catalog + diary, or just diary?
		action, err := GetPrompter().Ask(UserQuestion{
			Question: fmt.Sprintf("%s %.0f%s (%.0f ккал) — куда?",
				ca.Name, ca.Weight, ca.WeightUnit, diaryMacros.Calories),
			Options: []UserOption{
				{Label: "Каталог + дневник"},
				{Label: "Только дневник"},
				{Label: "Только каталог"},
			},
		})
		if err != nil {
			return "", err
		}

		var parts []string

		if action != "Только дневник" {
			sLabel := servingLabel
			sQty := float64(1)
			sUnit := "шт"
			if sLabel == "" {
				sLabel = "100g"
				sQty = 100
				sUnit = "г"
			}
			if err := catalogAddProduct(ca.Name, catalogMacros, "AI", sLabel, sQty, sUnit); err != nil {
				return "", err
			}
			parts = append(parts, fmt.Sprintf("+ В каталог: %s (%.0f ккал/%s)", ca.Name, catalogMacros.Calories, sLabel))
		}

		if action != "Только каталог" {
			mealArgs := map[string]any{
				"user":      username,
				"date":      date,
				"label":     ca.Name,
				"calories":  diaryMacros.Calories,
				"protein":   diaryMacros.Protein,
				"carbs":     diaryMacros.Carbs,
				"fats":      diaryMacros.Fats,
				"is_custom": true,
				"timestamp": nutriTimestamp(),
			}
			_, err := mcpCall("nutricalc__diary_add_meal", mealArgs)
			if err != nil {
				return "", fmt.Errorf("diary_add_meal: %w", err)
			}
			parts = append(parts, fmt.Sprintf("+ В дневник: %s %.0f%s (%.0f ккал, %.1fб, %.1fу, %.1fж)",
				ca.Name, ca.Weight, ca.WeightUnit, diaryMacros.Calories, diaryMacros.Protein, diaryMacros.Carbs, diaryMacros.Fats))

			stats, err := fetchAndFormatDayStats(username, date)
			if err == nil {
				parts = append(parts, "\n"+stats)
			}
		}

		return strings.Join(parts, "\n"), nil
	}

	// No weight — just add to catalog (macros are per 100g)
	if err := catalogAddProduct(ca.Name, m, "AI", "100g", 100, "г"); err != nil {
		return "", err
	}
	return fmt.Sprintf("+ В каталог: %s (%.0f ккал, %.1fб, %.1fу, %.1fж)",
		ca.Name, m.Calories, m.Protein, m.Carbs, m.Fats), nil
}

// estimateMacrosWithConfirm uses AI to estimate macros and asks user to confirm.
func estimateMacrosWithConfirm(name string) (macros, error) {
	if SubAgentFn == nil {
		return macros{}, fmt.Errorf("AI estimation not available for '%s'", name)
	}

	estimate, err := SubAgentFn(
		"You are a nutrition expert. Estimate macros per 100g for the given food. Return ONLY JSON: {\"calories\":N,\"protein\":N,\"carbs\":N,\"fats\":N}",
		fmt.Sprintf("Estimate macros per 100g for: %s", name),
	)
	if err != nil {
		return macros{}, fmt.Errorf("AI estimate failed: %w", err)
	}

	raw, err := extractJSON(estimate)
	if err != nil {
		return macros{}, fmt.Errorf("parse AI estimate: %w", err)
	}
	var m macros
	if err := json.Unmarshal(raw, &m); err != nil {
		return macros{}, fmt.Errorf("parse AI macros: %w", err)
	}

	if !AskAvailable() {
		return m, nil
	}

	answer, err := GetPrompter().Ask(UserQuestion{
		Question: fmt.Sprintf("%s — на 100г:\n%.0f ккал, %.1fб, %.1fу, %.1fж\nДобавить?",
			name, m.Calories, m.Protein, m.Carbs, m.Fats),
		Options: []UserOption{
			{Label: "Да"},
			{Label: "Нет"},
		},
	})
	if err != nil {
		return macros{}, err
	}
	if answer != "Да" {
		return macros{}, fmt.Errorf("отменено")
	}
	return m, nil
}

// --- Catalog-add macro parsing ---

// catalogAddInput is the parsed result of a multi-line catalog-add message.
type catalogAddInput struct {
	Name       string
	Macros     macros
	HasMacros  bool
	Weight     float64 // trailing weight in grams, 0 if absent
	WeightUnit string  // "г", "мл", etc.
}

// parseCatalogAdd detects multi-line input with inline macros and optional trailing weight.
// Returns nil if the text doesn't look like a catalog-add.
func parseCatalogAdd(text string) *catalogAddInput {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return nil
	}

	var nameLines []string
	var m macros
	foundMacro := false
	var weight float64
	var weightUnit string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if key, val, ok := parseMacroLine(line); ok {
			foundMacro = true
			switch key {
			case "cal":
				m.Calories = val
			case "pro":
				m.Protein = val
			case "carb":
				m.Carbs = val
			case "fat":
				m.Fats = val
			}
		} else if w, u, ok := parseWeightLine(line); ok {
			weight = w
			weightUnit = u
		} else {
			nameLines = append(nameLines, line)
		}
	}

	name := strings.Join(nameLines, " ")
	if name == "" || !foundMacro {
		return nil
	}
	return &catalogAddInput{
		Name:       name,
		Macros:     m,
		HasMacros:  true,
		Weight:     weight,
		WeightUnit: weightUnit,
	}
}

var macroLineRe = regexp.MustCompile(`^([a-zA-Zа-яА-ЯёЁ]+)[:\s]\s*(\d+[.,]?\d*)$`)

func parseMacroLine(line string) (key string, val float64, ok bool) {
	m := macroLineRe.FindStringSubmatch(line)
	if m == nil {
		return "", 0, false
	}
	word := strings.ToLower(m[1])
	valStr := strings.Replace(m[2], ",", ".", 1)
	fmt.Sscanf(valStr, "%f", &val)

	switch word {
	case "к", "кал", "калории", "ккал", "cal", "calories":
		return "cal", val, true
	case "б", "белки", "белок", "протеин", "protein", "p":
		return "pro", val, true
	case "у", "углеводы", "карбс", "carbs", "carb":
		return "carb", val, true
	case "ж", "жиры", "жир", "fat", "fats", "f":
		return "fat", val, true
	}
	return "", 0, false
}

// parseWeightLine checks if a line is just a weight, e.g. "30г", "150 g", "200мл".
var weightLineRe = regexp.MustCompile(`^(\d+[.,]?\d*)\s*(г|g|гр|мл|ml)\s*$`)

func parseWeightLine(line string) (weight float64, unit string, ok bool) {
	m := weightLineRe.FindStringSubmatch(line)
	if m == nil {
		return 0, "", false
	}
	valStr := strings.Replace(m[1], ",", ".", 1)
	fmt.Sscanf(valStr, "%f", &weight)
	unit = m[2]
	switch unit {
	case "g", "гр":
		unit = "г"
	}
	return weight, unit, true
}

// --- Username resolution ---

func resolveNutricalcUsername() (string, error) {
	// Try userinfo first (most reliable — persisted to file)
	if cfg := getUserInfoConfig(); cfg != nil {
		if entries, err := userInfoGet(cfg); err == nil {
			for _, key := range []string{"nutricalc_username", "eat_username"} {
				if e, ok := entries[key]; ok && e.Value != "" {
					return e.Value, nil
				}
			}
		}
	}

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

		// Save to userinfo (persistent, checked first on next call)
		if cfg := getUserInfoConfig(); cfg != nil {
			userInfoSet(cfg, "nutricalc_username", UserInfoEntry{
				Value:   username,
				OnlyFor: "eat",
			})
		}
		// Save to memory as fallback
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

// catalogAddProduct adds a product via catalog_add_product and then patches the
// serving with quantity/unit via catalog_update_item (the add endpoint only
// accepts serving_label, not quantity/unit).
func catalogAddProduct(name string, m macros, categoryName, servingLabel string, servingQty float64, servingUnit string) error {
	addArgs := map[string]any{
		"name":          name,
		"calories":      m.Calories,
		"protein":       m.Protein,
		"carbs":         m.Carbs,
		"fats":          m.Fats,
		"category_name": categoryName,
		"serving_label": servingLabel,
	}
	result, err := mcpCall("nutricalc__catalog_add_product", addArgs)
	if err != nil {
		return fmt.Errorf("catalog_add_product: %w", err)
	}

	// Parse response to get item ID and serving ID for the update call.
	var addResp struct {
		ID   string `json:"id"`
		Item struct {
			Servings []struct {
				ID string `json:"id"`
			} `json:"servings"`
		} `json:"item"`
	}
	if err := json.Unmarshal([]byte(result), &addResp); err != nil || addResp.ID == "" {
		return nil // added OK but can't patch serving — non-fatal
	}
	servingID := ""
	if len(addResp.Item.Servings) > 0 {
		servingID = addResp.Item.Servings[0].ID
	}
	if servingID == "" {
		return nil
	}

	// Patch the serving with quantity and unit.
	updateArgs := map[string]any{
		"id": addResp.ID,
		"servings": []map[string]any{{
			"id":       servingID,
			"label":    servingLabel,
			"quantity": servingQty,
			"unit":     servingUnit,
			"macros": map[string]any{
				"calories": m.Calories,
				"protein":  m.Protein,
				"carbs":    m.Carbs,
				"fats":     m.Fats,
			},
		}},
	}
	_, _ = mcpCall("nutricalc__catalog_update_item", updateArgs) // best-effort
	return nil
}
