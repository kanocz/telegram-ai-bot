package main

import "strings"

// parseThinkPrefix checks for a "/think" prefix in the query.
// Returns (true, remaining) if found, or (false, original) otherwise.
func parseThinkPrefix(s string) (bool, string) {
	s = strings.TrimSpace(s)
	if s == "/think" {
		return true, ""
	}
	if strings.HasPrefix(s, "/think ") || strings.HasPrefix(s, "/think\n") {
		return true, strings.TrimSpace(s[len("/think"):])
	}
	return false, s
}

// parseNothinkPrefix checks for a "/nothink" prefix in the query.
// Returns (true, remaining) if found, or (false, original) otherwise.
func parseNothinkPrefix(s string) (bool, string) {
	s = strings.TrimSpace(s)
	if s == "/nothink" {
		return true, ""
	}
	if strings.HasPrefix(s, "/nothink ") || strings.HasPrefix(s, "/nothink\n") {
		return true, strings.TrimSpace(s[len("/nothink"):])
	}
	return false, s
}

// parseMCPPrefix extracts MCP server names from a "/mcp name1,name2 ..." prefix.
// Returns (serverNames, remainingQuery). If no prefix, returns (nil, original).
func parseMCPPrefix(s string) ([]string, string) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "/mcp ") && !strings.HasPrefix(s, "/mcp\n") {
		return nil, s
	}
	rest := strings.TrimSpace(s[4:])

	// First token is comma-separated server names, rest is the query.
	// Separator can be space or newline (multi-line input from Telegram/CLI).
	sepIdx := strings.IndexAny(rest, " \n")
	if sepIdx < 0 {
		// "/mcp names" with no query — not a valid prefix
		return nil, s
	}
	spaceIdx := sepIdx

	namesStr := rest[:spaceIdx]
	query := strings.TrimSpace(rest[spaceIdx+1:])

	names := strings.Split(namesStr, ",")
	var result []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			result = append(result, n)
		}
	}
	if len(result) == 0 {
		return nil, s
	}
	return result, query
}
