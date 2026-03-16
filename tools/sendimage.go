package tools

import (
	"encoding/json"
	"fmt"
	"sync"
)

// ImageSender sends an image to the user (e.g. via Telegram).
type ImageSender interface {
	SendImage(dataURI string, caption string) error
}

// Goroutine-local image sender storage.
var imageSenderOverrides sync.Map // goroutineID -> ImageSender

// SetImageSender stores an ImageSender for the calling goroutine.
func SetImageSender(s ImageSender) { imageSenderOverrides.Store(goroutineID(), s) }

// ClearImageSender removes the ImageSender for the calling goroutine.
func ClearImageSender() { imageSenderOverrides.Delete(goroutineID()) }

// GetImageSender returns the ImageSender for the calling goroutine, or nil.
func GetImageSender() ImageSender {
	v, ok := imageSenderOverrides.Load(goroutineID())
	if !ok {
		return nil
	}
	return v.(ImageSender)
}

// ImageSenderAvailable returns true if an ImageSender is set for the current goroutine.
func ImageSenderAvailable() bool {
	_, ok := imageSenderOverrides.Load(goroutineID())
	return ok
}

type sendImageArgs struct {
	ImageID int    `json:"image_id"`
	Caption string `json:"caption"`
}

func init() {
	Register(&Tool{
		Def: Definition{
			Type: "function",
			Function: Function{
				Name: "send_image",
				Description: "Send an image to the user's chat. " +
					"Use this after capturing a camera snapshot or any tool that produces images. " +
					"Reference the image by its ID (shown in the tool result). " +
					"Only send images when the user would benefit from seeing them.",
				Parameters: Parameters{
					Type: "object",
					Properties: map[string]Property{
						"image_id": {
							Type:        "integer",
							Description: "The image ID from a previous tool result (e.g. 1, 2, 3)",
						},
						"caption": {
							Type:        "string",
							Description: "Optional caption for the image",
						},
					},
					Required: []string{"image_id"},
				},
			},
		},
		Execute: execSendImage,
	})
}

func execSendImage(args json.RawMessage) (string, error) {
	sender := GetImageSender()
	if sender == nil {
		return "", fmt.Errorf("send_image is not available in this mode")
	}

	var a sendImageArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.ImageID < 1 {
		return "", fmt.Errorf("image_id must be a positive integer")
	}

	dataURI, ok := GetSessionImage(a.ImageID)
	if !ok {
		return fmt.Sprintf("Image #%d not found. No image with this ID was captured in the current session.", a.ImageID), nil
	}

	if err := sender.SendImage(dataURI, a.Caption); err != nil {
		return "", fmt.Errorf("failed to send image: %w", err)
	}

	return fmt.Sprintf("Image #%d sent to user successfully.", a.ImageID), nil
}
