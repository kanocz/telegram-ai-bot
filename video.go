package main

import (
	"encoding/base64"
	"fmt"
	"math"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// VideoFrameConfig configures video-to-frames conversion.
// When set on a model, videos are converted to a series of JPEG frames
// instead of being sent as video_url (for models that lack native video support).
type VideoFrameConfig struct {
	MaxFrames  int `json:"maxFrames"`  // max overview frames (default 120)
	FrameWidth int `json:"frameWidth"` // resize width in px, 0=original (default 448)
}

// VideoFrame holds a single extracted video frame.
type VideoFrame struct {
	Timestamp float64 // seconds from video start
	DataURI   string  // data:image/jpeg;base64,...
}

// probeVideoDuration returns the duration of a video file in seconds.
func probeVideoDuration(path string) (float64, error) {
	out, err := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	return strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
}

// extractFrames extracts up to maxFrames evenly-spaced JPEG frames from a video.
// frameWidth controls resize (0 = original resolution).
// Returns the frames and the video duration.
func extractFrames(videoPath string, maxFrames, frameWidth int) ([]VideoFrame, float64, error) {
	if maxFrames <= 0 {
		maxFrames = 120
	}

	duration, err := probeVideoDuration(videoPath)
	if err != nil {
		return nil, 0, err
	}
	if duration <= 0 {
		return nil, 0, fmt.Errorf("video has zero duration")
	}

	fps := float64(maxFrames) / duration
	if fps > 30 {
		fps = 30
	}
	n := int(math.Ceil(fps * duration))
	if n > maxFrames {
		n = maxFrames
	}
	if n < 1 {
		n = 1
	}

	tmpDir, err := os.MkdirTemp("", "vf-*")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(tmpDir)

	vf := fmt.Sprintf("fps=%.6f", fps)
	if frameWidth > 0 {
		vf += fmt.Sprintf(",scale=%d:-2", frameWidth)
	}

	outPattern := filepath.Join(tmpDir, "f_%05d.jpg")
	cmd := exec.Command("ffmpeg",
		"-i", videoPath,
		"-vf", vf,
		"-frames:v", strconv.Itoa(n),
		"-q:v", "3",
		"-y",
		outPattern,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, 0, fmt.Errorf("ffmpeg: %w\n%s", err, out)
	}

	interval := 1.0 / fps
	var frames []VideoFrame
	for i := 1; i <= n; i++ {
		data, err := os.ReadFile(filepath.Join(tmpDir, fmt.Sprintf("f_%05d.jpg", i)))
		if err != nil {
			break
		}
		frames = append(frames, VideoFrame{
			Timestamp: float64(i-1) * interval,
			DataURI:   "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data),
		})
	}

	return frames, duration, nil
}

// extractFramesRange extracts frames from a specific time range at higher density.
func extractFramesRange(videoPath string, startSec, endSec float64, maxFrames, frameWidth int) ([]VideoFrame, error) {
	dur := endSec - startSec
	if dur <= 0 {
		return nil, fmt.Errorf("invalid time range: %.1f–%.1f", startSec, endSec)
	}
	if maxFrames <= 0 {
		maxFrames = 60
	}

	fps := float64(maxFrames) / dur
	if fps > 30 {
		fps = 30
	}
	n := int(math.Ceil(fps * dur))
	if n > maxFrames {
		n = maxFrames
	}
	if n < 1 {
		n = 1
	}

	tmpDir, err := os.MkdirTemp("", "vf-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	vf := fmt.Sprintf("fps=%.6f", fps)
	if frameWidth > 0 {
		vf += fmt.Sprintf(",scale=%d:-2", frameWidth)
	}

	outPattern := filepath.Join(tmpDir, "f_%05d.jpg")
	cmd := exec.Command("ffmpeg",
		"-ss", fmt.Sprintf("%.3f", startSec),
		"-i", videoPath,
		"-t", fmt.Sprintf("%.3f", dur),
		"-vf", vf,
		"-frames:v", strconv.Itoa(n),
		"-q:v", "2",
		"-y",
		outPattern,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w\n%s", err, out)
	}

	interval := 1.0 / fps
	var frames []VideoFrame
	for i := 1; i <= n; i++ {
		data, err := os.ReadFile(filepath.Join(tmpDir, fmt.Sprintf("f_%05d.jpg", i)))
		if err != nil {
			break
		}
		frames = append(frames, VideoFrame{
			Timestamp: startSec + float64(i-1)*interval,
			DataURI:   "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data),
		})
	}

	return frames, nil
}

// saveBytesToTempFile saves raw bytes to a temp file with extension from MIME type.
func saveBytesToTempFile(data []byte, mimeType string) (string, error) {
	ext := ".bin"
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		ext = exts[0]
	}
	f, err := os.CreateTemp("", "video-*"+ext)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	f.Close()
	return f.Name(), nil
}
