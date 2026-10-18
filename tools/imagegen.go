package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// --- Config ---

type imageGenConfig struct {
	URL string `json:"url"`
}

var imageGenCfgPath = "thenoise.json"

// SetImageGenConfigPath overrides the path to thenoise.json.
func SetImageGenConfigPath(path string) { imageGenCfgPath = path }

func getImageGenConfig() (*imageGenConfig, error) {
	data, err := os.ReadFile(imageGenCfgPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", imageGenCfgPath, err)
	}
	var cfg imageGenConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", imageGenCfgPath, err)
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("%s: url is empty", imageGenCfgPath)
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return &cfg, nil
}

// ImageGenAvailable returns true if thenoise.json is configured.
func ImageGenAvailable() bool {
	_, err := getImageGenConfig()
	return err == nil
}

// --- Tools ---

var imageGenClient = &http.Client{Timeout: 10 * time.Minute} // model load + slow models

func init() {
	common := map[string]any{
		"prompt":          map[string]any{"type": "string", "description": "Detailed image description in English"},
		"negative_prompt": map[string]any{"type": "string", "description": "What to avoid (optional)"},
		"width":           map[string]any{"type": "integer", "description": "Width in pixels (optional, model default)"},
		"height":          map[string]any{"type": "integer", "description": "Height in pixels (optional, model default)"},
		"steps":           map[string]any{"type": "integer", "description": "Sampling steps (optional, model default)"},
		"seed":            map[string]any{"type": "integer", "description": "Seed for reproducibility (optional, random by default)"},
	}
	editProps := map[string]any{
		"image_ids": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "integer"},
			"description": "IDs of source images (e.g. [1]). The first image sets the output size.",
		},
	}
	for k, v := range common {
		editProps[k] = v
	}

	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name: "image_generate",
				Description: "Generate an image from a text prompt. " +
					"The result is delivered to the user automatically and gets an image ID for image_edit.",
				Parameters: map[string]any{"type": "object", "properties": common, "required": []string{"prompt"}},
			},
		},
		Execute: func(args json.RawMessage) (string, error) { return execImageGen("/text2image", args) },
	})
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name: "image_edit",
				Description: "Edit one or more images by an instruction (e.g. 'make the sky purple'). " +
					"Images are referenced by ID: user attachments and earlier tool results show their IDs. " +
					"The result is delivered to the user automatically.",
				Parameters: map[string]any{"type": "object", "properties": editProps, "required": []string{"image_ids", "prompt"}},
			},
		},
		Execute: func(args json.RawMessage) (string, error) { return execImageGen("/edit", args) },
	})
	RegisterCommand(&Command{Name: "image", Handler: handleImageCommand})
}

func execImageGen(endpoint string, args json.RawMessage) (string, error) {
	var req map[string]any
	if err := json.Unmarshal(args, &req); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p, _ := req["prompt"].(string); p == "" {
		return "", fmt.Errorf("prompt is required")
	}

	if endpoint == "/edit" {
		ids, _ := req["image_ids"].([]any)
		if len(ids) == 0 {
			return "", fmt.Errorf("image_ids is required")
		}
		var uris []string
		for _, v := range ids {
			id, _ := v.(float64)
			uri, ok := GetSessionImage(int(id))
			if !ok {
				return fmt.Sprintf("Image #%v not found in the current session.", v), nil
			}
			uris = append(uris, uri)
		}
		delete(req, "image_ids")
		req["image"] = bareBase64(uris)
	}

	id, path, err := runImageGen(endpoint, req)
	if err != nil {
		return "", err
	}
	if path == "" {
		return fmt.Sprintf("Image #%d generated and sent to the user. Do not call send_image for it.", id), nil
	}
	return fmt.Sprintf("Image #%d generated and saved to %s. Tell the user the path.", id, path), nil
}

// bareBase64 strips the data URI prefix: TheNoise wants plain base64.
func bareBase64(uris []string) []string {
	out := make([]string, len(uris))
	for i, uri := range uris {
		if j := strings.Index(uri, ","); j >= 0 {
			uri = uri[j+1:]
		}
		out[i] = uri
	}
	return out
}

// runImageGen calls TheNoise and delivers the PNG: sent via ImageSender when
// one is set (path is empty then), otherwise saved to the current directory.
func runImageGen(endpoint string, req map[string]any) (id int, path string, err error) {
	cfg, err := getImageGenConfig()
	if err != nil {
		return 0, "", err
	}

	body, _ := json.Marshal(req)
	resp, err := imageGenClient.Post(cfg.URL+endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, "", fmt.Errorf("thenoise: %w", err)
	}
	defer resp.Body.Close()
	png, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("thenoise: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("thenoise: HTTP %d: %s", resp.StatusCode, clip(string(png), 500))
	}

	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	id = AddSessionImage(dataURI)
	prompt, _ := req["prompt"].(string)

	if sender := GetImageSender(); sender != nil {
		if err := sender.SendImage(dataURI, clip(prompt, 1000)); err != nil {
			return 0, "", fmt.Errorf("failed to send image: %w", err)
		}
		return id, "", nil
	}

	path = fmt.Sprintf("image-%s-%d.png", time.Now().Format("20060102-150405"), id)
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return 0, "", fmt.Errorf("save image: %w", err)
	}
	return id, path, nil
}

// handleImageCommand is /image: the prompt goes straight to TheNoise, no LLM.
// With attached images it edits them, otherwise generates a new one.
func handleImageCommand(ctx *CommandContext) (string, error) {
	prompt := strings.TrimSpace(ctx.Text)
	if prompt == "" {
		return "Usage: /image <prompt>; attach or reply to an image to edit it.", nil
	}
	endpoint := "/text2image"
	// Same as the TheNoise web UI sends when only a prompt is set.
	req := map[string]any{"prompt": prompt, "width": 1024, "height": 1024}
	if len(ctx.Images) > 0 {
		// Output size follows the first image.
		endpoint = "/edit"
		req = map[string]any{"prompt": prompt, "image": bareBase64(ctx.Images)}
	}
	_, path, err := runImageGen(endpoint, req)
	return path, err
}

// clip shortens s to at most n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
