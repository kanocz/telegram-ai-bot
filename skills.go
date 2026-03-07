package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai-webfetch/tools"
)

// parseSkillsPrefix extracts skill names from a "/skills name1,name2 ..." prefix.
// Returns (skillNames, remainingQuery). If no prefix, returns (nil, original).
func parseSkillsPrefix(s string) ([]string, string) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "/skills ") && !strings.HasPrefix(s, "/skills\n") {
		return nil, s
	}
	rest := strings.TrimSpace(s[7:])

	sepIdx := strings.IndexAny(rest, " \n")
	if sepIdx < 0 {
		return nil, s
	}

	namesStr := rest[:sepIdx]
	query := strings.TrimSpace(rest[sepIdx+1:])

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

// skillSearchDirs returns the default directories to search for skills.
// Global (home-based) dirs are searched first, then local (cwd-based).
func skillSearchDirs() []string {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()
	var dirs []string
	if home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".claude", "skills"),
			filepath.Join(home, ".agents", "skills"),
			filepath.Join(home, ".copilot", "skills"),
		)
	}
	if cwd != "" {
		dirs = append(dirs,
			filepath.Join(cwd, ".github", "skills"),
			filepath.Join(cwd, ".claude", "skills"),
			filepath.Join(cwd, ".agents", "skills"),
		)
	}
	return dirs
}

// findSkill searches for a skill across the given directories.
// Checks two layouts per directory:
//  1. dir/name.md        (flat file)
//  2. dir/name/SKILL.md  (directory with SKILL.md inside)
//
// Returns the first match. Error if not found anywhere.
func findSkill(dirs []string, name string) ([]byte, error) {
	for _, dir := range dirs {
		// flat: dir/name.md
		if data, err := os.ReadFile(filepath.Join(dir, name+".md")); err == nil {
			return data, nil
		}
		// directory: dir/name/SKILL.md
		if data, err := os.ReadFile(filepath.Join(dir, name, "SKILL.md")); err == nil {
			return data, nil
		}
	}
	return nil, fmt.Errorf("skill %q not found in any of: %s", name, strings.Join(dirs, ", "))
}

// parseSkillFrontmatter extracts YAML-like frontmatter from skill data.
// If the data starts with "---\n", it looks for a closing "---\n" and
// extracts "mcp:" values (comma-separated server names).
// Returns the body (without frontmatter) and any MCP server names found.
func parseSkillFrontmatter(data []byte) ([]byte, []string) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return data, nil
	}
	end := strings.Index(s[4:], "\n---\n")
	if end < 0 {
		return data, nil
	}
	frontmatter := s[4 : 4+end]
	body := []byte(s[4+end+5:])

	var mcpNames []string
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "mcp:") {
			val := strings.TrimSpace(line[4:])
			for _, name := range strings.Split(val, ",") {
				name = strings.TrimSpace(name)
				if name != "" {
					mcpNames = append(mcpNames, name)
				}
			}
		}
	}
	return body, mcpNames
}

// loadSkills reads skill markdown files and returns concatenated text
// plus any MCP server names from skill frontmatter.
func loadSkills(dirs []string, names []string) (string, []string, error) {
	var sb strings.Builder
	var allMCP []string
	for _, name := range names {
		data, err := findSkill(dirs, name)
		if err != nil {
			return "", nil, err
		}
		body, mcpNames := parseSkillFrontmatter(data)
		allMCP = append(allMCP, mcpNames...)
		sb.WriteString("\n\n## Skill: ")
		sb.WriteString(name)
		sb.WriteString("\n\n")
		sb.Write(body)
	}
	return sb.String(), dedup(allMCP, nil), nil
}

// parseCommandName extracts a potential command name from "/name rest".
// Returns (name, rest) or ("", "") if not a slash command.
func parseCommandName(s string) (string, string) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "/") {
		return "", ""
	}
	rest := s[1:]
	sepIdx := strings.IndexAny(rest, " \n")
	if sepIdx < 0 {
		return rest, ""
	}
	return rest[:sepIdx], strings.TrimSpace(rest[sepIdx+1:])
}

// Reserved slash-command names that must not be treated as skill shortcuts.
var reservedCommands = map[string]bool{
	"think": true, "nothink": true, "mcp": true, "skills": true,
	"news": true, "mail": true, "start": true, "help": true,
}

// parseSkillShortcut checks if query starts with "/name" where name
// matches an existing skill file (and is not a reserved command).
// Returns (skillName, remainingQuery) or ("", original) if no match.
func parseSkillShortcut(s string, skillDirs []string) (string, string) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "/") {
		return "", s
	}

	// Extract the command name (first word after /)
	rest := s[1:]
	sepIdx := strings.IndexAny(rest, " \n")
	var name, query string
	if sepIdx < 0 {
		name = rest
		query = ""
	} else {
		name = rest[:sepIdx]
		query = strings.TrimSpace(rest[sepIdx+1:])
	}

	if name == "" || reservedCommands[name] || tools.IsCommand(name) {
		return "", s
	}

	// Check if skill exists (without reading full content)
	if _, err := findSkill(skillDirs, name); err != nil {
		return "", s
	}

	return name, query
}
