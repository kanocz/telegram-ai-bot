package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// loadSkills reads skill markdown files and returns concatenated text.
// If dirs has one entry (explicit -skills-dir), only that dir is searched.
// Otherwise all default search dirs are used.
func loadSkills(dirs []string, names []string) (string, error) {
	var sb strings.Builder
	for _, name := range names {
		data, err := findSkill(dirs, name)
		if err != nil {
			return "", err
		}
		sb.WriteString("\n\n## Skill: ")
		sb.WriteString(name)
		sb.WriteString("\n\n")
		sb.Write(data)
	}
	return sb.String(), nil
}
