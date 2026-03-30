package tools

import (
	"strings"
	"testing"
)

func TestApplyUnifiedDiff_ExactMatch(t *testing.T) {
	original := "line1\nline2\nline3\nline4\nline5"
	patch := `--- a/file
+++ b/file
@@ -2,3 +2,3 @@
 line2
-line3
+line3_modified
 line4`

	result, err := applyUnifiedDiff(original, patch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "line1\nline2\nline3_modified\nline4\nline5"
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}

func TestApplyUnifiedDiff_FuzzyOffset(t *testing.T) {
	// The hunk header says line 2, but the actual content is at line 5.
	original := "a\nb\nc\nline2\nline3\nline4\nd"
	patch := `@@ -1,3 +1,3 @@
 line2
-line3
+line3_changed
 line4`

	result, err := applyUnifiedDiff(original, patch)
	if err != nil {
		t.Fatalf("unexpected error (fuzzy should find it): %v", err)
	}
	expected := "a\nb\nc\nline2\nline3_changed\nline4\nd"
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}

func TestApplyUnifiedDiff_TrailingWhitespace(t *testing.T) {
	// Original has trailing spaces, patch does not — should still match.
	original := "line1  \nline2\t\nline3\nline4"
	patch := `@@ -1,4 +1,4 @@
 line1
 line2
-line3
+line3_new
 line4`

	result, err := applyUnifiedDiff(original, patch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Context lines preserve the original content (with trailing spaces).
	if !strings.Contains(result, "line3_new") {
		t.Errorf("expected line3_new in result, got:\n%s", result)
	}
	if !strings.Contains(result, "line1  ") {
		t.Errorf("expected original trailing spaces preserved, got:\n%s", result)
	}
}

func TestApplyUnifiedDiff_MultipleHunks(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "original"
	}
	lines[4] = "target_a"
	lines[14] = "target_b"
	original := strings.Join(lines, "\n")

	patch := `@@ -4,3 +4,3 @@
 original
-target_a
+replaced_a
 original
@@ -14,3 +14,3 @@
 original
-target_b
+replaced_b
 original`

	result, err := applyUnifiedDiff(original, patch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "replaced_a") {
		t.Errorf("expected replaced_a in result")
	}
	if !strings.Contains(result, "replaced_b") {
		t.Errorf("expected replaced_b in result")
	}
	if strings.Contains(result, "target_a") || strings.Contains(result, "target_b") {
		t.Errorf("original targets should be removed")
	}
}

func TestApplyUnifiedDiff_InsertLines(t *testing.T) {
	original := "line1\nline2\nline3"
	patch := `@@ -2,1 +2,3 @@
 line2
+new_line_a
+new_line_b`

	result, err := applyUnifiedDiff(original, patch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "line1\nline2\nnew_line_a\nnew_line_b\nline3"
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}

func TestApplyUnifiedDiff_DeleteLines(t *testing.T) {
	original := "line1\nline2\nline3\nline4\nline5"
	patch := `@@ -2,3 +2,1 @@
-line2
-line3
-line4`

	result, err := applyUnifiedDiff(original, patch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "line1\nline5"
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}

func TestApplyUnifiedDiff_NoHunks(t *testing.T) {
	original := "line1"
	patch := "just some text with no hunks"

	_, err := applyUnifiedDiff(original, patch)
	if err == nil {
		t.Fatal("expected error for patch with no hunks")
	}
}

func TestApplyUnifiedDiff_ContextMismatchFails(t *testing.T) {
	// Content that doesn't exist anywhere in the file.
	original := "aaa\nbbb\nccc"
	patch := `@@ -1,2 +1,2 @@
 xxx
-yyy
+zzz`

	_, err := applyUnifiedDiff(original, patch)
	if err == nil {
		t.Fatal("expected error for completely mismatched context")
	}
}

func TestApplyUnifiedDiff_LargeFuzzyOffset(t *testing.T) {
	// Build a file with 150 filler lines, then the target content.
	var lines []string
	for i := 0; i < 150; i++ {
		lines = append(lines, "filler")
	}
	lines = append(lines, "ctx_before", "old_line", "ctx_after")
	original := strings.Join(lines, "\n")

	// Hunk header claims line 1 but the real content is at line 151.
	patch := `@@ -1,3 +1,3 @@
 ctx_before
-old_line
+new_line
 ctx_after`

	result, err := applyUnifiedDiff(original, patch)
	if err != nil {
		t.Fatalf("expected fuzzy search to find content at offset 150: %v", err)
	}
	if !strings.Contains(result, "new_line") {
		t.Errorf("expected new_line in result")
	}
	if strings.Contains(result, "old_line") {
		t.Errorf("old_line should be replaced")
	}
}

func TestApplyUnifiedDiff_EmptyLinesInPatch(t *testing.T) {
	original := "start\n\nmiddle\n\nend"
	patch := `@@ -1,5 +1,5 @@
 start

-middle
+middle_changed

 end`

	result, err := applyUnifiedDiff(original, patch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "start\n\nmiddle_changed\n\nend"
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}

func TestApplyUnifiedDiff_DiffHeadersIgnored(t *testing.T) {
	original := "hello\nworld"
	patch := `diff --git a/file.txt b/file.txt
index abc123..def456 100644
--- a/file.txt
+++ b/file.txt
@@ -1,2 +1,2 @@
 hello
-world
+universe`

	result, err := applyUnifiedDiff(original, patch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "hello\nuniverse"
	if result != expected {
		t.Errorf("got:\n%s\nwant:\n%s", result, expected)
	}
}

func TestApplyUnifiedDiff_AlreadyApplied(t *testing.T) {
	// File already has the patched version.
	original := "line1\nline2\nline3_modified\nline4\nline5"
	// Patch tries to change line3 → line3_modified, but it's already done.
	patch := `@@ -2,3 +2,3 @@
 line2
-line3
+line3_modified
 line4`

	_, err := applyUnifiedDiff(original, patch)
	if err == nil {
		t.Fatal("expected error for already-applied patch")
	}
	if !strings.Contains(err.Error(), "already applied") {
		t.Errorf("expected 'already applied' in error, got: %v", err)
	}
}

func TestApplyUnifiedDiff_AlreadyAppliedMultiline(t *testing.T) {
	// Simulates the real-world case: LLM generates a patch that adds several lines,
	// but those lines are already in the file from a previous application.
	original := `}

function updateStandardPreview() {
    // FIXED settings matching the generator
    const fixedSettings = {
        lpiMin: 300,
        lpiMax: 800,
    };
    const currentSettings = SettingsStorage.load();
    if (currentSettings.activeDevice) {
        fixedSettings.activeDevice = currentSettings.activeDevice;
    }
    drawGridToCanvas('standardPreviewCanvas', fixedSettings);
}

function downloadTestGridXCS() {`

	// LLM tries to apply the same transformation again.
	patch := `@@ -3,4 +3,12 @@
 function updateStandardPreview() {
-    const currentSettings = SettingsStorage.load();
-    drawGridToCanvas('standardPreviewCanvas', currentSettings);
+    // FIXED settings matching the generator
+    const fixedSettings = {
+        lpiMin: 300,
+        lpiMax: 800,
+    };
+    const currentSettings = SettingsStorage.load();
+    if (currentSettings.activeDevice) {
+        fixedSettings.activeDevice = currentSettings.activeDevice;
+    }
+    drawGridToCanvas('standardPreviewCanvas', fixedSettings);
 }`

	_, err := applyUnifiedDiff(original, patch)
	if err == nil {
		t.Fatal("expected error for already-applied patch")
	}
	if !strings.Contains(err.Error(), "already applied") {
		t.Errorf("expected 'already applied' in error, got: %v", err)
	}
}

func TestNormalizeLine(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"hello  ", "hello"},
		{"hello\t", "hello"},
		{"hello \t ", "hello"},
		{"  hello  ", "  hello"},
		{"\t", ""},
		{"", ""},
	}
	for _, c := range cases {
		got := normalizeLine(c.in)
		if got != c.want {
			t.Errorf("normalizeLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLinesMatchFuzzy(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"hello", "hello", true},
		{"hello  ", "hello", true},
		{"hello", "hello\t", true},
		{"hello  ", "hello\t\t", true},
		{"  hello", "  hello  ", true},
		{"hello", "world", false},
		{"  hello", "hello", false}, // leading whitespace matters
	}
	for _, c := range cases {
		got := linesMatchFuzzy(c.a, c.b)
		if got != c.want {
			t.Errorf("linesMatchFuzzy(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
