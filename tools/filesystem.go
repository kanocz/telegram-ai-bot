package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// RegisterFilesystem registers filesystem tools sandboxed to root.
// If readWrite is true, write tools are also registered.
func RegisterFilesystem(root string, readWrite bool) {
	// Resolve root to absolute + real path once at startup.
	absRoot, err := filepath.Abs(root)
	if err != nil {
		panic("filesystem: cannot resolve root: " + err.Error())
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		panic("filesystem: cannot resolve root symlinks: " + err.Error())
	}

	safe := func(userPath string) (string, error) {
		return safePath(realRoot, userPath)
	}
	safeNew := func(userPath string) (string, error) {
		return safeNewPath(realRoot, userPath)
	}

	// --- read-only tools ---

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "fs_list",
				Description: "List files and directories at the given path (defaults to root). Returns names with / suffix for directories and (size) for files.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path": {Type: "string", Description: "Directory path to list (relative to sandbox root, default: root)"},
					},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			json.Unmarshal(args, &p)
			if p.Path == "" {
				p.Path = "."
			}
			target, err := safe(p.Path)
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(target)
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			for _, e := range entries {
				if e.IsDir() {
					sb.WriteString(e.Name() + "/\n")
				} else {
					info, _ := e.Info()
					size := int64(0)
					if info != nil {
						size = info.Size()
					}
					sb.WriteString(fmt.Sprintf("%s (%d bytes)\n", e.Name(), size))
				}
			}
			if sb.Len() == 0 {
				return "(empty directory)", nil
			}
			return sb.String(), nil
		},
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name: "fs_read",
				Description: `Read the contents of a file. Rejects binary files. Output includes line numbers.
IMPORTANT: For large files, ALWAYS use "line" and "count" to read only the needed portion.
Use fs_grep to find the relevant line numbers first, then read just that range.
Example: to read a function at line 150, use {"path": "file.js", "line": 145, "count": 40} — not the whole file.
Reading a 200KB file wastes most of the available context and leaves no room for follow-up work.`,
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path":   {Type: "string", Description: "File path to read (relative to sandbox root)"},
						"line":   {Type: "integer", Description: "Start reading from this line number (1-based). Preferred over offset for text files."},
						"count":  {Type: "integer", Description: "Number of lines to read (default: 100 when 'line' is set, otherwise entire file up to 512KB). Use with 'line' to read a specific range."},
						"offset": {Type: "integer", Description: "Byte offset to start reading from (default: 0). Prefer 'line' for text files."},
						"limit":  {Type: "integer", Description: "Maximum bytes to read (default/max: 524288). Prefer 'count' for text files."},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Path   string `json:"path"`
				Line   int    `json:"line"`
				Count  int    `json:"count"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			json.Unmarshal(args, &p)

			target, err := safe(p.Path)
			if err != nil {
				return "", err
			}

			// Line-based reading mode.
			if p.Line > 0 || p.Count > 0 {
				return readLines(target, p.Line, p.Count)
			}

			const maxRead = 512 * 1024
			limit := maxRead
			if p.Limit > 0 && p.Limit < maxRead {
				limit = p.Limit
			}

			f, err := os.Open(target)
			if err != nil {
				return "", err
			}
			defer f.Close()

			if p.Offset > 0 {
				if _, err := f.Seek(int64(p.Offset), 0); err != nil {
					return "", err
				}
			}

			buf := make([]byte, limit)
			n, err := f.Read(buf)
			if err != nil && n == 0 {
				return "", err
			}
			buf = buf[:n]

			// Reject binary: check first 8KB for NUL bytes.
			check := buf
			if len(check) > 8192 {
				check = check[:8192]
			}
			if bytes.ContainsRune(check, 0) {
				return "", fmt.Errorf("file appears to be binary")
			}

			// For large results, warn about wasted context.
			content := string(buf)
			info, _ := os.Stat(target)
			if info != nil && info.Size() > 50*1024 {
				totalLines := strings.Count(content, "\n") + 1
				return fmt.Sprintf("[WARNING: large file (%d bytes, ~%d lines). Next time use 'line' and 'count' to read only the needed section.]\n\n%s", info.Size(), totalLines, content), nil
			}

			return content, nil
		},
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "fs_info",
				Description: "Get file/directory metadata: name, size, modification time, permissions, whether it is a directory.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path": {Type: "string", Description: "Path to inspect (relative to sandbox root)"},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			json.Unmarshal(args, &p)

			target, err := safe(p.Path)
			if err != nil {
				return "", err
			}
			info, err := os.Stat(target)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("name: %s\nsize: %d\nmod_time: %s\npermissions: %s\nis_dir: %v",
				info.Name(), info.Size(), info.ModTime().Format("2006-01-02 15:04:05"),
				info.Mode().Perm().String(), info.IsDir()), nil
		},
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name: "fs_grep",
				Description: `Search file contents by text or regex pattern. Recursively searches files under the given path.
Returns matching lines with file paths and line numbers. Skips binary files and hidden directories (.*).
Example output:
  src/main.go:42: func main() {
  src/util.go:10: // helper function`,
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"pattern": {Type: "string", Description: "Search pattern (plain text or regex depending on 'regex' flag)"},
						"path":    {Type: "string", Description: "Directory or file to search in (relative to sandbox root, default: root)"},
						"regex":   {Type: "boolean", Description: "Treat pattern as a regular expression (default: false, plain text substring match)"},
						"ignore_case": {Type: "boolean", Description: "Case-insensitive search (default: false)"},
						"include":     {Type: "string", Description: "Glob pattern to filter file names, e.g. '*.go' or '*.{js,ts}' (default: all files)"},
						"context":     {Type: "integer", Description: "Number of context lines before and after each match (default: 0)"},
						"max_results": {Type: "integer", Description: "Maximum number of matching lines to return (default: 200)"},
					},
					Required: []string{"pattern"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Pattern    string `json:"pattern"`
				Path       string `json:"path"`
				Regex      bool   `json:"regex"`
				IgnoreCase bool   `json:"ignore_case"`
				Include    string `json:"include"`
				Context    int    `json:"context"`
				MaxResults int    `json:"max_results"`
			}
			json.Unmarshal(args, &p)

			if p.Pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			if p.MaxResults <= 0 {
				p.MaxResults = 200
			}
			if p.Context < 0 {
				p.Context = 0
			}

			searchPath := "."
			if p.Path != "" {
				searchPath = p.Path
			}
			target, err := safe(searchPath)
			if err != nil {
				return "", err
			}

			// Build the matcher function.
			matcher, err := buildMatcher(p.Pattern, p.Regex, p.IgnoreCase)
			if err != nil {
				return "", err
			}

			// Build the include glob matcher.
			var includeMatch func(string) bool
			if p.Include != "" {
				includeMatch = func(name string) bool {
					ok, _ := filepath.Match(p.Include, name)
					return ok
				}
			}

			info, err := os.Stat(target)
			if err != nil {
				return "", err
			}

			type match struct {
				file string
				line int
				text string
			}
			var matches []match
			done := false

			grepFile := func(filePath, relPath string) {
				if done {
					return
				}
				data, err := os.ReadFile(filePath)
				if err != nil {
					return
				}
				// Skip binary files.
				check := data
				if len(check) > 8192 {
					check = check[:8192]
				}
				if bytes.ContainsRune(check, 0) {
					return
				}

				lines := strings.Split(string(data), "\n")
				for i, line := range lines {
					if matcher(line) {
						if p.Context == 0 {
							matches = append(matches, match{relPath, i + 1, line})
						} else {
							start := i - p.Context
							if start < 0 {
								start = 0
							}
							end := i + p.Context
							if end >= len(lines) {
								end = len(lines) - 1
							}
							for j := start; j <= end; j++ {
								prefix := " "
								if j == i {
									prefix = ">"
								}
								matches = append(matches, match{relPath, j + 1, prefix + lines[j]})
								if len(matches) >= p.MaxResults {
									done = true
									return
								}
							}
							// Separator between context groups.
							matches = append(matches, match{"--", 0, ""})
							if len(matches) >= p.MaxResults {
								done = true
								return
							}
							continue
						}
						if len(matches) >= p.MaxResults {
							done = true
							return
						}
					}
				}
			}

			if !info.IsDir() {
				rel, _ := filepath.Rel(realRoot, target)
				grepFile(target, rel)
			} else {
				filepath.Walk(target, func(path string, fi os.FileInfo, err error) error {
					if err != nil || done {
						return err
					}
					// Skip hidden directories.
					if fi.IsDir() && strings.HasPrefix(fi.Name(), ".") && path != target {
						return filepath.SkipDir
					}
					if fi.IsDir() || fi.Size() == 0 {
						return nil
					}
					// Skip large files (>2MB).
					if fi.Size() > 2*1024*1024 {
						return nil
					}
					if includeMatch != nil && !includeMatch(fi.Name()) {
						return nil
					}
					rel, _ := filepath.Rel(realRoot, path)
					grepFile(path, rel)
					return nil
				})
			}

			if len(matches) == 0 {
				return "no matches found", nil
			}

			var sb strings.Builder
			for _, m := range matches {
				if m.file == "--" {
					sb.WriteString("--\n")
					continue
				}
				sb.WriteString(fmt.Sprintf("%s:%d: %s\n", m.file, m.line, m.text))
			}
			if done {
				sb.WriteString(fmt.Sprintf("\n(results truncated at %d matches)\n", p.MaxResults))
			}
			return sb.String(), nil
		},
	})

	// --- write tools ---
	if !readWrite {
		return
	}

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "fs_write",
				Description: "Write content to a file, creating it (and parent directories) if needed. Overwrites existing content.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path":    {Type: "string", Description: "File path to write (relative to sandbox root)"},
						"content": {Type: "string", Description: "Content to write to the file"},
					},
					Required: []string{"path", "content"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			json.Unmarshal(args, &p)

			target, err := safeNew(p.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return "", err
			}
			if err := os.WriteFile(target, []byte(p.Content), 0644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
		},
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "fs_append",
				Description: "Append content to a file, creating it if needed.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path":    {Type: "string", Description: "File path to append to (relative to sandbox root)"},
						"content": {Type: "string", Description: "Content to append"},
					},
					Required: []string{"path", "content"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			json.Unmarshal(args, &p)

			target, err := safeNew(p.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return "", err
			}
			f, err := os.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return "", err
			}
			defer f.Close()
			n, err := f.WriteString(p.Content)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("appended %d bytes to %s", n, p.Path), nil
		},
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "fs_patch",
				Description: "Apply a unified diff patch to a file. The patch should be in unified diff format with @@ hunk headers.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path":  {Type: "string", Description: "File path to patch (relative to sandbox root)"},
						"patch": {Type: "string", Description: "Unified diff patch content"},
					},
					Required: []string{"path", "patch"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Path  string `json:"path"`
				Patch string `json:"patch"`
			}
			json.Unmarshal(args, &p)

			target, err := safe(p.Path)
			if err != nil {
				return "", err
			}
			original, err := os.ReadFile(target)
			if err != nil {
				return "", err
			}
			result, err := applyUnifiedDiff(string(original), p.Patch)
			if err != nil {
				return "", fmt.Errorf("patch failed: %w", err)
			}
			if err := os.WriteFile(target, []byte(result), 0644); err != nil {
				return "", err
			}
			return fmt.Sprintf("patched %s", p.Path), nil
		},
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "fs_mkdir",
				Description: "Create a directory (and any necessary parents).",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path": {Type: "string", Description: "Directory path to create (relative to sandbox root)"},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			json.Unmarshal(args, &p)

			target, err := safeNew(p.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(target, 0755); err != nil {
				return "", err
			}
			return fmt.Sprintf("created directory %s", p.Path), nil
		},
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "fs_rm",
				Description: "Remove a file or directory. Use recursive=true for non-empty directories. Cannot delete the sandbox root.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path":      {Type: "string", Description: "Path to remove (relative to sandbox root)"},
						"recursive": {Type: "boolean", Description: "Remove directories recursively (default: false)"},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Path      string `json:"path"`
				Recursive bool   `json:"recursive"`
			}
			json.Unmarshal(args, &p)

			target, err := safe(p.Path)
			if err != nil {
				return "", err
			}
			// Prevent deleting the sandbox root
			if target == realRoot {
				return "", fmt.Errorf("cannot delete sandbox root")
			}
			if p.Recursive {
				if err := os.RemoveAll(target); err != nil {
					return "", err
				}
			} else {
				if err := os.Remove(target); err != nil {
					return "", err
				}
			}
			return fmt.Sprintf("removed %s", p.Path), nil
		},
	})
}

// readLines reads a range of lines from a text file, returning them with line numbers.
func readLines(path string, startLine, count int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	// Reject binary.
	check := data
	if len(check) > 8192 {
		check = check[:8192]
	}
	if bytes.ContainsRune(check, 0) {
		return "", fmt.Errorf("file appears to be binary")
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	if startLine < 1 {
		startLine = 1
	}
	if startLine > totalLines {
		return fmt.Sprintf("(file has %d lines, requested start at line %d)", totalLines, startLine), nil
	}

	if count <= 0 {
		count = 100
	}

	endLine := startLine + count - 1
	if endLine > totalLines {
		endLine = totalLines
	}

	var sb strings.Builder
	for i := startLine; i <= endLine; i++ {
		sb.WriteString(fmt.Sprintf("%d\t%s\n", i, lines[i-1]))
	}

	if endLine < totalLines {
		sb.WriteString(fmt.Sprintf("\n[showing lines %d–%d of %d total]\n", startLine, endLine, totalLines))
	}

	return sb.String(), nil
}

// safePath resolves userPath relative to root and ensures the result stays within root.
// It resolves symlinks to prevent escaping via symlink traversal.
func safePath(root, userPath string) (string, error) {
	var abs string
	if filepath.IsAbs(userPath) {
		abs = filepath.Clean(userPath)
	} else {
		abs = filepath.Join(root, userPath)
	}

	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}

	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside sandbox", userPath)
	}
	return real, nil
}

// safeNewPath resolves a path for a new file/dir that may not exist yet.
// It validates that the parent directory is within the sandbox.
func safeNewPath(root, userPath string) (string, error) {
	var abs string
	if filepath.IsAbs(userPath) {
		abs = filepath.Clean(userPath)
	} else {
		abs = filepath.Join(root, userPath)
	}

	// Try resolving the full path first (file might already exist)
	real, err := filepath.EvalSymlinks(abs)
	if err == nil {
		if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q is outside sandbox", userPath)
		}
		return real, nil
	}

	// File doesn't exist: resolve parent directory
	parent := filepath.Dir(abs)
	realParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		// Parent doesn't exist either — check that the constructed path stays in root
		cleaned := filepath.Clean(abs)
		if cleaned != root && !strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q is outside sandbox", userPath)
		}
		return cleaned, nil
	}

	if realParent != root && !strings.HasPrefix(realParent, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside sandbox", userPath)
	}
	return filepath.Join(realParent, filepath.Base(abs)), nil
}

// applyUnifiedDiff applies a unified diff patch to the original content.
func applyUnifiedDiff(original, patch string) (string, error) {
	lines := strings.Split(original, "\n")
	hunks, err := parseHunks(patch)
	if err != nil {
		return "", err
	}

	// Apply hunks in reverse order so earlier line numbers remain valid.
	for i := len(hunks) - 1; i >= 0; i-- {
		h := hunks[i]
		lines, err = applyHunk(lines, h)
		if err != nil {
			return "", fmt.Errorf("hunk %d: %w", i+1, err)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// normalizeLine trims trailing whitespace for fuzzy comparison.
func normalizeLine(s string) string {
	return strings.TrimRight(s, " \t\r")
}

// linesMatchFuzzy compares two lines with trailing-whitespace normalization.
func linesMatchFuzzy(a, b string) bool {
	return normalizeLine(a) == normalizeLine(b)
}

type hunk struct {
	origStart int // 1-based
	origCount int
	lines     []diffLine
}

type diffLine struct {
	op   byte // ' ', '+', '-'
	text string
}

func parseHunks(patch string) ([]hunk, error) {
	patchLines := strings.Split(patch, "\n")
	var hunks []hunk

	i := 0
	for i < len(patchLines) {
		line := patchLines[i]
		if !strings.HasPrefix(line, "@@") {
			i++
			continue
		}
		// Parse @@ -origStart,origCount +newStart,newCount @@
		h, err := parseHunkHeader(line)
		if err != nil {
			return nil, err
		}
		i++
		for i < len(patchLines) {
			l := patchLines[i]
			if strings.HasPrefix(l, "@@") || strings.HasPrefix(l, "diff ") || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ") {
				break
			}
			if len(l) == 0 {
				// Treat empty lines in the diff as context lines with empty text
				h.lines = append(h.lines, diffLine{op: ' ', text: ""})
			} else {
				op := l[0]
				if op != '+' && op != '-' && op != ' ' {
					// Treat as context
					h.lines = append(h.lines, diffLine{op: ' ', text: l})
				} else {
					h.lines = append(h.lines, diffLine{op: op, text: l[1:]})
				}
			}
			i++
		}
		hunks = append(hunks, h)
	}

	if len(hunks) == 0 {
		return nil, fmt.Errorf("no hunks found in patch")
	}
	return hunks, nil
}

func parseHunkHeader(line string) (hunk, error) {
	// Format: @@ -origStart[,origCount] +newStart[,newCount] @@
	line = strings.TrimPrefix(line, "@@")
	idx := strings.Index(line[1:], "@@")
	if idx < 0 {
		return hunk{}, fmt.Errorf("malformed hunk header: %q", line)
	}
	header := strings.TrimSpace(line[:idx+1])
	parts := strings.Fields(header)
	if len(parts) < 2 {
		return hunk{}, fmt.Errorf("malformed hunk header: %q", header)
	}

	origPart := strings.TrimPrefix(parts[0], "-")
	origStart, origCount, err := parseRange(origPart)
	if err != nil {
		return hunk{}, fmt.Errorf("parse orig range %q: %w", origPart, err)
	}

	return hunk{origStart: origStart, origCount: origCount}, nil
}

func parseRange(s string) (start, count int, err error) {
	if idx := strings.IndexByte(s, ','); idx >= 0 {
		start, err = strconv.Atoi(s[:idx])
		if err != nil {
			return
		}
		count, err = strconv.Atoi(s[idx+1:])
		return
	}
	start, err = strconv.Atoi(s)
	count = 1
	return
}

// tryApplyHunkAt tries to apply the hunk at the given 0-based start position.
// Returns the new lines and true if successful, or nil and false on mismatch.
func tryApplyHunkAt(lines []string, h hunk, start int) ([]string, bool) {
	pos := start
	var newLines []string
	for _, dl := range h.lines {
		switch dl.op {
		case ' ':
			if pos >= len(lines) {
				return nil, false
			}
			if !linesMatchFuzzy(lines[pos], dl.text) {
				return nil, false
			}
			newLines = append(newLines, lines[pos]) // keep original line
			pos++
		case '-':
			if pos >= len(lines) {
				return nil, false
			}
			if !linesMatchFuzzy(lines[pos], dl.text) {
				return nil, false
			}
			pos++
		case '+':
			newLines = append(newLines, dl.text)
		}
	}

	result := make([]string, 0, len(lines)-(pos-start)+len(newLines))
	result = append(result, lines[:start]...)
	result = append(result, newLines...)
	result = append(result, lines[pos:]...)
	return result, true
}

const maxFuzz = 200 // maximum lines to search above/below the stated position

func applyHunk(lines []string, h hunk) ([]string, error) {
	// origStart is 1-based; convert to 0-based index
	start := h.origStart - 1
	if start < 0 {
		start = 0
	}

	// Try exact position first.
	if result, ok := tryApplyHunkAt(lines, h, start); ok {
		return result, nil
	}

	// Fuzzy: search nearby offsets ±1, ±2, ... ±maxFuzz.
	for offset := 1; offset <= maxFuzz; offset++ {
		// Try below
		if candidate := start + offset; candidate >= 0 && candidate < len(lines) {
			if result, ok := tryApplyHunkAt(lines, h, candidate); ok {
				return result, nil
			}
		}
		// Try above
		if candidate := start - offset; candidate >= 0 && candidate < len(lines) {
			if result, ok := tryApplyHunkAt(lines, h, candidate); ok {
				return result, nil
			}
		}
	}

	// Check if the patch was already applied.
	if alreadyApplied(lines, h, start) {
		return nil, fmt.Errorf("patch already applied (the added lines are already present near line %d)", h.origStart)
	}

	// Build a diagnostic message with the first context/remove line that would fail.
	return nil, hunkDiagnostic(lines, h, start)
}

// hunkDiagnostic produces a helpful error explaining why a hunk couldn't be applied.
func hunkDiagnostic(lines []string, h hunk, start int) error {
	for i, dl := range h.lines {
		if dl.op == '+' {
			continue
		}
		pos := start + contextIndex(h.lines[:i])
		if pos >= len(lines) {
			return fmt.Errorf("line %d out of range (file has %d lines); could not find matching context within ±%d lines", pos+1, len(lines), maxFuzz)
		}
		if !linesMatchFuzzy(lines[pos], dl.text) {
			return fmt.Errorf("could not find matching context within ±%d lines of line %d; first mismatch: expected %q, nearest %q", maxFuzz, h.origStart, dl.text, lines[pos])
		}
	}
	return fmt.Errorf("could not apply hunk at line %d within ±%d lines", h.origStart, maxFuzz)
}

// alreadyApplied checks whether a hunk's result (context + added lines, without removed lines)
// is already present in the file, indicating the patch was previously applied.
func alreadyApplied(lines []string, h hunk, hint int) bool {
	// Build the expected post-patch sequence: context lines + added lines (skip removed).
	var expected []string
	for _, dl := range h.lines {
		if dl.op == ' ' || dl.op == '+' {
			expected = append(expected, dl.text)
		}
	}
	if len(expected) == 0 {
		return false
	}

	// Search near the hinted position.
	searchStart := hint - maxFuzz
	if searchStart < 0 {
		searchStart = 0
	}
	searchEnd := hint + maxFuzz
	if searchEnd > len(lines)-len(expected) {
		searchEnd = len(lines) - len(expected)
	}

	for i := searchStart; i <= searchEnd; i++ {
		match := true
		for j, exp := range expected {
			if i+j >= len(lines) || !linesMatchFuzzy(lines[i+j], exp) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// contextIndex counts how many original-side lines (context + remove) precede index i.
func contextIndex(dls []diffLine) int {
	n := 0
	for _, dl := range dls {
		if dl.op == ' ' || dl.op == '-' {
			n++
		}
	}
	return n
}

// buildMatcher returns a function that tests whether a line matches the search pattern.
func buildMatcher(pattern string, isRegex, ignoreCase bool) (func(string) bool, error) {
	if isRegex {
		expr := pattern
		if ignoreCase {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		return re.MatchString, nil
	}
	// Plain text substring match.
	if ignoreCase {
		patLower := strings.ToLower(pattern)
		return func(line string) bool {
			return strings.Contains(strings.ToLower(line), patLower)
		}, nil
	}
	return func(line string) bool {
		return strings.Contains(line, pattern)
	}, nil
}
