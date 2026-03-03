package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
				Name:        "fs_read",
				Description: "Read the contents of a file. Returns up to 512KB. Rejects binary files.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"path":   {Type: "string", Description: "File path to read (relative to sandbox root)"},
						"offset": {Type: "integer", Description: "Byte offset to start reading from (default: 0)"},
						"limit":  {Type: "integer", Description: "Maximum bytes to read (default/max: 524288)"},
					},
					Required: []string{"path"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Path   string `json:"path"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			json.Unmarshal(args, &p)

			target, err := safe(p.Path)
			if err != nil {
				return "", err
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

			// Reject binary: check first 8KB for NUL bytes
			check := buf
			if len(check) > 8192 {
				check = check[:8192]
			}
			if bytes.ContainsRune(check, 0) {
				return "", fmt.Errorf("file appears to be binary")
			}

			return string(buf), nil
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

func applyHunk(lines []string, h hunk) ([]string, error) {
	// origStart is 1-based; convert to 0-based index
	start := h.origStart - 1
	if start < 0 {
		start = 0
	}

	// Verify context and remove lines
	pos := start
	var newLines []string
	for _, dl := range h.lines {
		switch dl.op {
		case ' ':
			if pos >= len(lines) {
				return nil, fmt.Errorf("context line %d out of range (file has %d lines)", pos+1, len(lines))
			}
			if lines[pos] != dl.text {
				return nil, fmt.Errorf("context mismatch at line %d: expected %q, got %q", pos+1, dl.text, lines[pos])
			}
			newLines = append(newLines, dl.text)
			pos++
		case '-':
			if pos >= len(lines) {
				return nil, fmt.Errorf("remove line %d out of range", pos+1)
			}
			if lines[pos] != dl.text {
				return nil, fmt.Errorf("remove mismatch at line %d: expected %q, got %q", pos+1, dl.text, lines[pos])
			}
			pos++
		case '+':
			newLines = append(newLines, dl.text)
		}
	}

	result := make([]string, 0, len(lines))
	result = append(result, lines[:start]...)
	result = append(result, newLines...)
	result = append(result, lines[pos:]...)
	return result, nil
}
