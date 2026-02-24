package main

import "strings"

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
