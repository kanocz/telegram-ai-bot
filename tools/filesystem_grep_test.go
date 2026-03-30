package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// setupGrepSandbox creates a temp directory with test files and registers filesystem tools.
func setupGrepSandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// src/main.go
	srcDir := filepath.Join(dir, "src")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "main.go"), []byte(
		"package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello World\")\n}\n"), 0644)

	// src/util.go
	os.WriteFile(filepath.Join(srcDir, "util.go"), []byte(
		"package main\n\n// helper function\nfunc add(a, b int) int {\n\treturn a + b\n}\n\nfunc Subtract(a, b int) int {\n\treturn a - b\n}\n"), 0644)

	// data/notes.txt
	dataDir := filepath.Join(dir, "data")
	os.MkdirAll(dataDir, 0755)
	os.WriteFile(filepath.Join(dataDir, "notes.txt"), []byte(
		"TODO: fix the bug\nDONE: write tests\ntodo: lowercase variant\n"), 0644)

	// .hidden/secret.txt — should be skipped
	hiddenDir := filepath.Join(dir, ".hidden")
	os.MkdirAll(hiddenDir, 0755)
	os.WriteFile(filepath.Join(hiddenDir, "secret.txt"), []byte("secret pattern\n"), 0644)

	// binary file — should be skipped
	os.WriteFile(filepath.Join(dir, "binary.dat"), []byte("text\x00binary\x00data"), 0644)

	RegisterFilesystem(dir, false)
	return dir
}

func runGrep(t *testing.T, args map[string]interface{}) string {
	t.Helper()
	tool, ok := Get("fs_grep")
	if !ok {
		t.Fatal("fs_grep tool not registered")
	}
	raw, _ := json.Marshal(args)
	result, err := tool.Execute(raw)
	if err != nil {
		t.Fatalf("fs_grep error: %v", err)
	}
	return result
}

func TestFsGrep_PlainText(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	result := runGrep(t, map[string]interface{}{"pattern": "fmt.Println"})
	if result == "no matches found" {
		t.Fatal("expected matches for fmt.Println")
	}
	if !containsSubstr(result, "main.go") {
		t.Errorf("expected main.go in results, got:\n%s", result)
	}
	if !containsSubstr(result, "Hello World") {
		t.Errorf("expected Hello World in results, got:\n%s", result)
	}
}

func TestFsGrep_Regex(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	result := runGrep(t, map[string]interface{}{"pattern": `func\s+\w+\(`, "regex": true})
	if result == "no matches found" {
		t.Fatal("expected regex matches")
	}
	// Should match func main(), func add(), func Subtract()
	if !containsSubstr(result, "main()") {
		t.Errorf("expected main() in results, got:\n%s", result)
	}
	if !containsSubstr(result, "add(") {
		t.Errorf("expected add( in results, got:\n%s", result)
	}
}

func TestFsGrep_IgnoreCase(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	result := runGrep(t, map[string]interface{}{"pattern": "todo", "ignore_case": true})
	if result == "no matches found" {
		t.Fatal("expected case-insensitive matches")
	}
	// Should match both "TODO:" and "todo:"
	if !containsSubstr(result, "TODO: fix the bug") {
		t.Errorf("expected TODO match, got:\n%s", result)
	}
	if !containsSubstr(result, "todo: lowercase") {
		t.Errorf("expected lowercase todo match, got:\n%s", result)
	}
}

func TestFsGrep_IncludeGlob(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	// Search only .go files for "return"
	result := runGrep(t, map[string]interface{}{"pattern": "return", "include": "*.go"})
	if result == "no matches found" {
		t.Fatal("expected matches in .go files")
	}
	if containsSubstr(result, "notes.txt") {
		t.Errorf("should not search txt files with *.go include, got:\n%s", result)
	}
}

func TestFsGrep_ContextLines(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	result := runGrep(t, map[string]interface{}{"pattern": "add(a, b int)", "context": 1})
	if result == "no matches found" {
		t.Fatal("expected matches with context")
	}
	// Should include the line before and after the match
	if !containsSubstr(result, "helper function") || !containsSubstr(result, "return a + b") {
		t.Errorf("expected context lines, got:\n%s", result)
	}
}

func TestFsGrep_SkipsHiddenDirs(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	result := runGrep(t, map[string]interface{}{"pattern": "secret pattern"})
	if result != "no matches found" {
		t.Errorf("expected hidden directory to be skipped, got:\n%s", result)
	}
}

func TestFsGrep_SkipsBinaryFiles(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	result := runGrep(t, map[string]interface{}{"pattern": "binary"})
	if result != "no matches found" {
		t.Errorf("expected binary files to be skipped, got:\n%s", result)
	}
}

func TestFsGrep_SubdirectoryScope(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	result := runGrep(t, map[string]interface{}{"pattern": "return", "path": "src"})
	if result == "no matches found" {
		t.Fatal("expected matches in src/")
	}
	if containsSubstr(result, "notes.txt") || containsSubstr(result, "data/") {
		t.Errorf("should only search within src/, got:\n%s", result)
	}
}

func TestFsGrep_SingleFile(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	result := runGrep(t, map[string]interface{}{"pattern": "Subtract", "path": "src/util.go"})
	if result == "no matches found" {
		t.Fatal("expected match in single file")
	}
	if !containsSubstr(result, "Subtract") {
		t.Errorf("expected Subtract in results, got:\n%s", result)
	}
}

func TestFsGrep_MaxResults(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	result := runGrep(t, map[string]interface{}{"pattern": "a", "max_results": 3})
	lines := 0
	for _, c := range result {
		if c == '\n' {
			lines++
		}
	}
	// 3 matches + truncation notice = at most 5 lines
	if lines > 6 {
		t.Errorf("expected at most ~5 output lines with max_results=3, got %d lines:\n%s", lines, result)
	}
}

func TestFsGrep_InvalidRegex(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	tool, ok := Get("fs_grep")
	if !ok {
		t.Fatal("fs_grep not registered")
	}
	raw, _ := json.Marshal(map[string]interface{}{"pattern": "[invalid", "regex": true})
	_, err := tool.Execute(raw)
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestFsGrep_EmptyPattern(t *testing.T) {
	setupGrepSandbox(t)
	defer deregisterAll()

	tool, ok := Get("fs_grep")
	if !ok {
		t.Fatal("fs_grep not registered")
	}
	raw, _ := json.Marshal(map[string]interface{}{"pattern": ""})
	_, err := tool.Execute(raw)
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

func TestBuildMatcher_PlainText(t *testing.T) {
	m, err := buildMatcher("hello", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !m("say hello world") {
		t.Error("should match substring")
	}
	if m("say HELLO world") {
		t.Error("should be case-sensitive")
	}
}

func TestBuildMatcher_PlainTextIgnoreCase(t *testing.T) {
	m, err := buildMatcher("hello", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !m("say HELLO world") {
		t.Error("should match case-insensitively")
	}
}

func TestBuildMatcher_Regex(t *testing.T) {
	m, err := buildMatcher(`\d{3}-\d{4}`, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !m("call 123-4567") {
		t.Error("should match regex")
	}
	if m("no numbers here") {
		t.Error("should not match")
	}
}

func TestBuildMatcher_RegexIgnoreCase(t *testing.T) {
	m, err := buildMatcher("error", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !m("ERROR: something") {
		t.Error("should match case-insensitively with regex")
	}
}

// helpers

func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func deregisterAll() {
	// Clear registry to avoid interference between tests.
	for k := range registry {
		delete(registry, k)
	}
}
