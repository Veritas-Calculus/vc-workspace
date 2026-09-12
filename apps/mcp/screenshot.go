package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/jpeg"

	"github.com/Veritas-Calculus/vc-workspace/internal/computer"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Image bytes belong only in MCP ImageContent. Keeping them out of typed output
// prevents clients from receiving the same Base64 twice as structured JSON/text.
type screenshotOutput struct {
	SchemaVersion int                `json:"schema_version"`
	RequestID     string             `json:"request_id"`
	OK            bool               `json:"ok"`
	Screenshot    screenshotMetadata `json:"screenshot"`
}

type screenshotMetadata struct {
	ContentType   string                 `json:"content_type"`
	Width         int                    `json:"width"`
	Height        int                    `json:"height"`
	SHA256        string                 `json:"sha256"`
	DesktopBounds computer.DesktopBounds `json:"desktop_bounds"`
}

func screenshotToolResult(response computer.Response) (*mcp.CallToolResult, screenshotOutput, error) {
	invalid := func() (*mcp.CallToolResult, screenshotOutput, error) {
		// Do not echo Guest-controlled error messages or screenshot contents.
		return nil, screenshotOutput{}, errors.New("invalid desktop screenshot; verify the Guest agent version and retry the observation")
	}
	shot := response.Screenshot
	if !response.OK || response.SchemaVersion != computer.SchemaVersion || response.RequestID == "" || shot == nil || shot.DesktopBounds == nil ||
		shot.ContentType != "image/jpeg" || len(shot.Data) == 0 || len(shot.Data) > 12*1024*1024 || shot.Width < 1 || shot.Width > 3840 || shot.Height < 1 || shot.Height > 16384 {
		return invalid()
	}
	bounds := *shot.DesktopBounds
	if bounds.Width < 1 || bounds.Width > 16384 || bounds.Height < 1 || bounds.Height > 16384 ||
		bounds.X < -16384 || bounds.X > 16384 || bounds.Y < -16384 || bounds.Y > 16384 {
		return invalid()
	}
	data, err := base64.StdEncoding.Strict().DecodeString(shot.Data)
	if err != nil {
		return invalid()
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != shot.SHA256 {
		return invalid()
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != shot.Width || config.Height != shot.Height {
		return invalid()
	}
	output := screenshotOutput{
		SchemaVersion: response.SchemaVersion, RequestID: response.RequestID, OK: true,
		Screenshot: screenshotMetadata{
			ContentType: shot.ContentType, Width: shot.Width, Height: shot.Height,
			SHA256: shot.SHA256, DesktopBounds: bounds,
		},
	}
	// A compact text copy preserves metadata for MCP clients that don't yet read
	// structuredContent. The SDK derives outputSchema from screenshotOutput.
	metadata, err := json.Marshal(output)
	if err != nil {
		return invalid()
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{MIMEType: shot.ContentType, Data: data},
		&mcp.TextContent{Text: string(metadata)},
	}}, output, nil
}
