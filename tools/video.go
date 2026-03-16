package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
)

// --- Video state management (goroutine-local) ---

type videoState struct {
	Path       string  // path to video file (original or temp)
	Duration   float64 // video duration in seconds
	FrameWidth int     // overview frame width (0 = original)
	MaxFrames  int     // max overview frames
}

var videoStore sync.Map

// SetVideoState stores video file info for the current goroutine.
// Called when a video is converted to overview frames.
func SetVideoState(path string, duration float64, frameWidth, maxFrames int) {
	videoStore.Store(goroutineID(), &videoState{
		Path: path, Duration: duration, FrameWidth: frameWidth, MaxFrames: maxFrames,
	})
}

// ClearVideoState removes video state for the current goroutine.
func ClearVideoState() {
	videoStore.Delete(goroutineID())
}

// VideoAvailable returns true if a video is loaded for the current goroutine.
func VideoAvailable() bool {
	_, ok := videoStore.Load(goroutineID())
	return ok
}

func getVideoState() *videoState {
	v, ok := videoStore.Load(goroutineID())
	if !ok {
		return nil
	}
	return v.(*videoState)
}

// --- Video frame strip signal ---
// When video_get_frames executes, it signals that previous video frames
// should be stripped from the message history to save context.

var videoFrameStrip sync.Map

// SetVideoFrameStrip signals that old video frames should be stripped.
func SetVideoFrameStrip() {
	videoFrameStrip.Store(goroutineID(), true)
}

// TakeVideoFrameStrip checks and clears the strip signal.
func TakeVideoFrameStrip() bool {
	_, ok := videoFrameStrip.LoadAndDelete(goroutineID())
	return ok
}

// --- Frame extraction callback (set by main) ---

// VideoFrameResult holds a single extracted frame.
type VideoFrameResult struct {
	Timestamp float64
	DataURI   string
}

// VideoFramesFn extracts frames from a time range. Set by main.go.
var VideoFramesFn func(videoPath string, startSec, endSec float64, maxFrames, frameWidth int) ([]VideoFrameResult, error)

// --- Helper functions ---

// FormatTimestamp formats seconds as M:SS or H:MM:SS.
func FormatTimestamp(seconds float64) string {
	total := int(math.Round(seconds))
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// ParseTimeStr parses a time string like "1:23", "1:23.5", "83.5", or "1:23:45".
func ParseTimeStr(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 2:
		m, err1 := strconv.ParseFloat(parts[0], 64)
		sec, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil {
			return 0, fmt.Errorf("invalid time: %q", s)
		}
		return m*60 + sec, nil
	case 3:
		h, err1 := strconv.ParseFloat(parts[0], 64)
		m, err2 := strconv.ParseFloat(parts[1], 64)
		sec, err3 := strconv.ParseFloat(parts[2], 64)
		if err1 != nil || err2 != nil || err3 != nil {
			return 0, fmt.Errorf("invalid time: %q", s)
		}
		return h*3600 + m*60 + sec, nil
	}
	return 0, fmt.Errorf("invalid time: %q", s)
}

// --- Tool registration ---

func init() {
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name:        "video_get_frames",
				Description: "Extract frames from a specific time range of the attached video at higher frame density. Use after reviewing the overview frames to zoom into an interesting segment. Previous video frames will be removed from context to save space.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"start_time":          {Type: "string", Description: "Start time (e.g. '1:23', '83.5', '0:00')"},
						"end_time":            {Type: "string", Description: "End time (e.g. '1:27', '87.0', '2:35')"},
						"max_frames":          {Type: "integer", Description: "Maximum number of frames to extract (default: auto, typically 60)"},
						"original_resolution": {Type: "boolean", Description: "Use original video resolution instead of overview thumbnail size (default: false)"},
					},
					Required: []string{"start_time", "end_time"},
				},
			},
		},
		Execute: executeVideoGetFrames,
	})
}

func executeVideoGetFrames(args json.RawMessage) (string, error) {
	var params struct {
		StartTime          string `json:"start_time"`
		EndTime            string `json:"end_time"`
		MaxFrames          int    `json:"max_frames"`
		OriginalResolution bool   `json:"original_resolution"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	vs := getVideoState()
	if vs == nil {
		return "", fmt.Errorf("no video available in current session")
	}
	if VideoFramesFn == nil {
		return "", fmt.Errorf("video frame extraction not configured")
	}

	start, err := ParseTimeStr(params.StartTime)
	if err != nil {
		return "", fmt.Errorf("invalid start_time: %w", err)
	}
	end, err := ParseTimeStr(params.EndTime)
	if err != nil {
		return "", fmt.Errorf("invalid end_time: %w", err)
	}

	// Clamp to video bounds
	if start < 0 {
		start = 0
	}
	if end > vs.Duration {
		end = vs.Duration
	}

	frameWidth := vs.FrameWidth
	if params.OriginalResolution {
		frameWidth = 0
	}

	maxFrames := params.MaxFrames
	if maxFrames <= 0 {
		maxFrames = 60
	}

	frames, err := VideoFramesFn(vs.Path, start, end, maxFrames, frameWidth)
	if err != nil {
		return "", err
	}

	// Set pending images and signal strip
	var uris []string
	for _, f := range frames {
		uris = append(uris, f.DataURI)
	}
	SetPendingImages(uris)
	SetVideoFrameStrip()

	// Build result text
	duration := end - start
	fps := float64(len(frames)) / duration
	result := fmt.Sprintf("Extracted %d frames from %s to %s (%.1fs, ~%.1f fps).\n",
		len(frames), FormatTimestamp(start), FormatTimestamp(end), duration, fps)
	result += fmt.Sprintf("Frames are attached as images in chronological order (%s to %s).",
		FormatTimestamp(frames[0].Timestamp), FormatTimestamp(frames[len(frames)-1].Timestamp))

	return result, nil
}
