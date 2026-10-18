package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageEdit(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/edit" {
			t.Errorf("path = %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte("PNGDATA"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := filepath.Join(dir, "thenoise.json")
	os.WriteFile(cfg, []byte(`{"url":"`+srv.URL+`/"}`), 0o644)
	SetImageGenConfigPath(cfg)
	t.Chdir(dir)
	defer ClearSessionImages()

	AddSessionImage("data:image/jpeg;base64,QUJD")
	res, err := execImageGen("/edit", json.RawMessage(`{"prompt":"blue sky","image_ids":[1],"steps":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if imgs, _ := got["image"].([]any); len(imgs) != 1 || imgs[0] != "QUJD" {
		t.Errorf("image = %v, want [QUJD]", got["image"])
	}
	if _, ok := got["image_ids"]; ok || got["steps"] != 4.0 {
		t.Errorf("request = %v", got)
	}
	if !strings.Contains(res, "Image #2") {
		t.Errorf("result = %q", res)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "image-*-2.png"))
	if len(files) != 1 {
		t.Fatalf("saved files = %v", files)
	}
	if b, _ := os.ReadFile(files[0]); string(b) != "PNGDATA" {
		t.Errorf("file = %q", b)
	}
}

func TestImageCommand(t *testing.T) {
	var path string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		got = nil
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte("PNG"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := filepath.Join(dir, "thenoise.json")
	os.WriteFile(cfg, []byte(`{"url":"`+srv.URL+`"}`), 0o644)
	SetImageGenConfigPath(cfg)
	t.Chdir(dir)
	defer ClearSessionImages()

	if _, err := handleImageCommand(&CommandContext{Text: "фея в цветах"}); err != nil {
		t.Fatal(err)
	}
	if path != "/text2image" || got["prompt"] != "фея в цветах" || got["width"] != 1024.0 {
		t.Errorf("generate: %s %v", path, got)
	}

	res, err := handleImageCommand(&CommandContext{Text: "yellow hair", Images: []string{"data:image/png;base64,QUJD"}})
	if err != nil {
		t.Fatal(err)
	}
	if imgs, _ := got["image"].([]any); path != "/edit" || len(imgs) != 1 || imgs[0] != "QUJD" || got["width"] != nil {
		t.Errorf("edit: %s %v", path, got)
	}
	if !strings.HasPrefix(res, "image-") {
		t.Errorf("result = %q, want saved path", res)
	}
}
