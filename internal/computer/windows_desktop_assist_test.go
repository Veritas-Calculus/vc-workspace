package computer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type windowsDesktopAssistCommand struct {
	FrameSHA256 string `json:"frame_sha256"`
	Command     string `json:"command"`
	X           int    `json:"x"`
	Y           int    `json:"y"`
}

func freshWindowsAssistFrame(captured, now time.Time) bool {
	return !now.Before(captured) && now.Sub(captured) < time.Minute
}

func parseWindowsDesktopAssist(raw []byte, frame *ScreenshotResult) (windowsDesktopAssistCommand, error) {
	var command windowsDesktopAssistCommand
	if len(raw) == 0 || len(raw) > 4096 || frame == nil || frame.DesktopBounds == nil || frame.Width <= 0 || frame.Height <= 0 {
		return command, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&command) != nil || decoder.Decode(new(any)) != io.EOF || command.FrameSHA256 != frame.SHA256 || len(command.FrameSHA256) != 64 {
		return command, ErrInvalid
	}
	switch command.Command {
	case "click":
		if command.X < 0 || command.Y < 0 || command.X >= frame.Width || command.Y >= frame.Height {
			return command, ErrInvalid
		}
	case "refresh", "continue":
		if command.X != 0 || command.Y != 0 {
			return command, ErrInvalid
		}
	default:
		return command, ErrInvalid
	}
	return command, nil
}

// An explicit, human/agent-assisted acceptance escape hatch, not automatic
// consent. Each click requires inspection of a new private screenshot and a
// one-use command bound to its digest. No registry/region/privacy policy edits,
// keyboard/shell input or interaction with a non-fixture account is possible.
func assistWindowsHelperDesktop(t *testing.T, screenshot func(context.Context) *ScreenshotResult, click func(int, int)) {
	t.Helper()
	directory, err := os.MkdirTemp("", "vcw-windows-desktop-assist-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("private desktop assist directory: " + directory)
	// This is operator inspection time, not an input execution deadline. Fresh
	// frame clicks still expire after one minute; each dispatched action keeps
	// its original short protocol budget. Slow first-login screens can require
	// several read/inspect/click round trips before the foreground assertion.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	for number := 1; number <= 20 && time.Now().Before(deadline); number++ {
		frame := screenshot(ctx)
		if frame == nil || frame.ContentType != "image/jpeg" || frame.DesktopBounds == nil {
			t.Fatal("invalid assist screenshot")
		}
		pixels, err := base64.StdEncoding.DecodeString(frame.Data)
		if err != nil || len(pixels) > 16*1024*1024 || fmt.Sprintf("%x", sha256.Sum256(pixels)) != frame.SHA256 {
			t.Fatal("assist screenshot integrity mismatch")
		}
		dimensions, err := jpeg.DecodeConfig(bytes.NewReader(pixels))
		if err != nil || dimensions.Width != frame.Width || dimensions.Height != frame.Height {
			t.Fatal("assist screenshot dimension mismatch")
		}
		stem := filepath.Join(directory, fmt.Sprintf("frame-%02d", number))
		if err := os.WriteFile(stem+".jpg", pixels, 0600); err != nil {
			t.Fatal(err)
		}
		captured := time.Now()
		metadata, _ := json.Marshal(map[string]any{"frame_sha256": frame.SHA256, "width": frame.Width, "height": frame.Height, "desktop_bounds": frame.DesktopBounds, "captured_unix_ms": captured.UnixMilli(), "click_before_unix_ms": captured.Add(time.Minute).UnixMilli()})
		if err := os.WriteFile(stem+".json", metadata, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("inspect %s.jpg; provide %s.command.json with this frame_sha256 and click/refresh/continue", stem, stem)
		var raw []byte
		for time.Now().Before(deadline) {
			info, err := os.Lstat(stem + ".command.json")
			if err == nil {
				if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 4096 {
					t.Fatal("unsafe assist command file")
				}
				raw, err = os.ReadFile(stem + ".command.json")
				if err != nil {
					t.Fatal(err)
				}
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
				t.Fatal("desktop assist canceled")
			case <-time.After(250 * time.Millisecond):
			}
		}
		command, err := parseWindowsDesktopAssist(raw, frame)
		if err != nil {
			t.Fatal("invalid or missing one-use assist command", err)
		}
		if command.Command == "continue" {
			t.Log("desktop assistance complete; resuming real foreground/input assertions")
			return
		}
		if command.Command == "click" {
			if !freshWindowsAssistFrame(captured, time.Now()) {
				// Discard, never replay this click onto the next image. The
				// operator must inspect that image and submit a new command.
				// Keep the original total assist deadline and frame limit.
				t.Log("expired frame-bound click discarded; a new screenshot and explicit command are required")
				continue
			}
			bounds := frame.DesktopBounds
			click(bounds.X+command.X*bounds.Width/frame.Width, bounds.Y+command.Y*bounds.Height/frame.Height)
			t.Log("one frame-bound fixture click dispatched")
		}
		time.Sleep(750 * time.Millisecond)
	}
	t.Fatal("desktop assist exceeded its bounded window")
}

func TestWindowsDesktopAssistIsBoundedAndFrameSpecific(t *testing.T) {
	when := time.Now()
	if !freshWindowsAssistFrame(when, when) || !freshWindowsAssistFrame(when, when.Add(59*time.Second)) {
		t.Fatal("fresh inspection frame rejected")
	}
	for _, elapsed := range []time.Duration{-time.Millisecond, time.Minute, 2 * time.Minute} {
		if freshWindowsAssistFrame(when, when.Add(elapsed)) {
			t.Fatal("stale or future inspection frame accepted")
		}
	}
	frame := &ScreenshotResult{SHA256: fmt.Sprintf("%064d", 1), Width: 1280, Height: 720, DesktopBounds: &DesktopBounds{Width: 1280, Height: 720}}
	encode := func(command, hash string, x, y int) []byte {
		raw, _ := json.Marshal(windowsDesktopAssistCommand{hash, command, x, y})
		return raw
	}
	for _, command := range []string{"refresh", "continue", "click"} {
		if _, err := parseWindowsDesktopAssist(encode(command, frame.SHA256, 0, 0), frame); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range [][]byte{nil, encode("click", "wrong", 0, 0), encode("shell", frame.SHA256, 0, 0), encode("click", frame.SHA256, 1280, 0), encode("click", frame.SHA256, -1, 0), encode("click", frame.SHA256, 0, 720), encode("continue", frame.SHA256, 1, 0), append(encode("continue", frame.SHA256, 0, 0), []byte(" {}")...)} {
		if _, err := parseWindowsDesktopAssist(raw, frame); err == nil {
			t.Fatal("unsafe assist command accepted")
		}
	}
}
