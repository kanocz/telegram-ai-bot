package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// RegisterGit registers git history tools for the repository at repoPath.
func RegisterGit(repoPath string) {
	repo, err := git.PlainOpenWithOptions(repoPath, &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		panic("git: cannot open repository: " + err.Error())
	}

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "git_log",
				Description: "Show git commit log. Returns short hash, author, date, and message for each commit.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"limit": {Type: "integer", Description: "Maximum number of commits to show (default: 20, max: 100)"},
						"path":  {Type: "string", Description: "Only show commits affecting this file/directory path"},
						"since": {Type: "string", Description: "Show commits after this date (YYYY-MM-DD)"},
						"until": {Type: "string", Description: "Show commits before this date (YYYY-MM-DD)"},
					},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Limit int    `json:"limit"`
				Path  string `json:"path"`
				Since string `json:"since"`
				Until string `json:"until"`
			}
			json.Unmarshal(args, &p)

			if p.Limit <= 0 {
				p.Limit = 20
			}
			if p.Limit > 100 {
				p.Limit = 100
			}

			opts := &git.LogOptions{
				Order: git.LogOrderCommitterTime,
			}
			if p.Path != "" {
				opts.PathFilter = func(s string) bool {
					return s == p.Path || strings.HasPrefix(s, p.Path+"/")
				}
			}
			if p.Since != "" {
				t, err := time.Parse("2006-01-02", p.Since)
				if err == nil {
					opts.Since = &t
				}
			}
			if p.Until != "" {
				t, err := time.Parse("2006-01-02", p.Until)
				if err == nil {
					opts.Until = &t
				}
			}

			iter, err := repo.Log(opts)
			if err != nil {
				return "", fmt.Errorf("git log: %w", err)
			}
			defer iter.Close()

			var sb strings.Builder
			count := 0
			iter.ForEach(func(c *object.Commit) error {
				if count >= p.Limit {
					return fmt.Errorf("stop")
				}
				short := c.Hash.String()[:7]
				date := c.Author.When.Format("2006-01-02 15:04")
				msg := strings.SplitN(c.Message, "\n", 2)[0]
				sb.WriteString(fmt.Sprintf("%s %s %s\n  %s\n", short, c.Author.Name, date, msg))
				count++
				return nil
			})

			if sb.Len() == 0 {
				return "(no commits found)", nil
			}
			return sb.String(), nil
		},
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "git_show",
				Description: "Show details of a specific commit: metadata, file change stats, and optionally the full diff patch.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"commit": {Type: "string", Description: "Commit hash (full or short prefix)"},
						"diff":   {Type: "boolean", Description: "Include full diff patch (default: false, only show file stats)"},
					},
					Required: []string{"commit"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Commit string `json:"commit"`
				Diff   bool   `json:"diff"`
			}
			json.Unmarshal(args, &p)

			commit, err := resolveCommit(repo, p.Commit)
			if err != nil {
				return "", err
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("commit %s\n", commit.Hash.String()))
			sb.WriteString(fmt.Sprintf("Author: %s <%s>\n", commit.Author.Name, commit.Author.Email))
			sb.WriteString(fmt.Sprintf("Date:   %s\n\n", commit.Author.When.Format("2006-01-02 15:04:05 -0700")))
			sb.WriteString("    " + strings.ReplaceAll(strings.TrimSpace(commit.Message), "\n", "\n    "))
			sb.WriteString("\n\n")

			commitTree, err := commit.Tree()
			if err != nil {
				return sb.String(), nil
			}

			var parentTree *object.Tree
			if commit.NumParents() > 0 {
				parent, err := commit.Parent(0)
				if err == nil {
					parentTree, _ = parent.Tree()
				}
			}

			if parentTree == nil {
				// Root commit — diff against empty tree
				parentTree = &object.Tree{}
			}

			changes, err := parentTree.Diff(commitTree)
			if err != nil {
				return sb.String(), nil
			}

			patch, err := changes.Patch()
			if err != nil {
				return sb.String(), nil
			}

			// File stats
			for _, fp := range patch.FilePatches() {
				from, to := fp.Files()
				name := ""
				if to != nil {
					name = to.Path()
				} else if from != nil {
					name = from.Path()
				}
				var adds, dels int
				for _, chunk := range fp.Chunks() {
					for _, line := range strings.Split(chunk.Content(), "\n") {
						if line == "" {
							continue
						}
						switch chunk.Type() {
						case diff.Add:
							adds++
						case diff.Delete:
							dels++
						}
					}
				}
				sb.WriteString(fmt.Sprintf(" %s | +%d -%d\n", name, adds, dels))
			}

			if p.Diff {
				sb.WriteString("\n")
				diffStr := patch.String()
				const maxDiff = 100 * 1024
				if len(diffStr) > maxDiff {
					diffStr = diffStr[:maxDiff] + "\n[...truncated at 100KB]"
				}
				sb.WriteString(diffStr)
			}

			return sb.String(), nil
		},
	})

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "git_diff",
				Description: "Show diff between two commits, or between a commit and its parent. Optionally filter by path. Truncates at 100KB.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"commit": {Type: "string", Description: "Target commit hash (full or short prefix)"},
						"base":   {Type: "string", Description: "Base commit hash to diff from (default: parent of commit)"},
						"path":   {Type: "string", Description: "Only show changes for this file/directory path"},
					},
					Required: []string{"commit"},
				},
			},
		},
		Execute: func(args json.RawMessage) (string, error) {
			var p struct {
				Commit string `json:"commit"`
				Base   string `json:"base"`
				Path   string `json:"path"`
			}
			json.Unmarshal(args, &p)

			commit, err := resolveCommit(repo, p.Commit)
			if err != nil {
				return "", fmt.Errorf("resolve commit: %w", err)
			}

			commitTree, err := commit.Tree()
			if err != nil {
				return "", fmt.Errorf("commit tree: %w", err)
			}

			var baseTree *object.Tree
			if p.Base != "" {
				baseCommit, err := resolveCommit(repo, p.Base)
				if err != nil {
					return "", fmt.Errorf("resolve base: %w", err)
				}
				baseTree, err = baseCommit.Tree()
				if err != nil {
					return "", fmt.Errorf("base tree: %w", err)
				}
			} else {
				if commit.NumParents() > 0 {
					parent, err := commit.Parent(0)
					if err == nil {
						baseTree, _ = parent.Tree()
					}
				}
				if baseTree == nil {
					baseTree = &object.Tree{}
				}
			}

			changes, err := baseTree.Diff(commitTree)
			if err != nil {
				return "", fmt.Errorf("diff: %w", err)
			}

			patch, err := changes.Patch()
			if err != nil {
				return "", fmt.Errorf("patch: %w", err)
			}

			diffStr := patch.String()

			// Filter by path if requested
			if p.Path != "" {
				diffStr = filterDiffByPath(diffStr, p.Path)
			}

			const maxDiff = 100 * 1024
			if len(diffStr) > maxDiff {
				diffStr = diffStr[:maxDiff] + "\n[...truncated at 100KB]"
			}

			if diffStr == "" {
				return "(no changes)", nil
			}
			return diffStr, nil
		},
	})
}

// resolveCommit resolves a full or abbreviated commit hash to a Commit object.
func resolveCommit(repo *git.Repository, hashStr string) (*object.Commit, error) {
	// Try as full hash first
	hash := plumbing.NewHash(hashStr)
	c, err := repo.CommitObject(hash)
	if err == nil {
		return c, nil
	}

	// Try as prefix — iterate commits to find match
	iter, err := repo.CommitObjects()
	if err != nil {
		return nil, fmt.Errorf("cannot iterate commits: %w", err)
	}
	defer iter.Close()

	var match *object.Commit
	iter.ForEach(func(c *object.Commit) error {
		if strings.HasPrefix(c.Hash.String(), hashStr) {
			match = c
			return fmt.Errorf("found")
		}
		return nil
	})

	if match != nil {
		return match, nil
	}
	return nil, fmt.Errorf("commit %q not found", hashStr)
}

// filterDiffByPath filters a unified diff string to only include sections for the given path.
func filterDiffByPath(diffStr, path string) string {
	lines := strings.Split(diffStr, "\n")
	var result []string
	include := false

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git") {
			// Check if this diff section matches the path
			include = strings.Contains(line, " a/"+path) || strings.Contains(line, " b/"+path) ||
				strings.Contains(line, " a/"+path+"/") || strings.Contains(line, " b/"+path+"/")
		}
		if include {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}
