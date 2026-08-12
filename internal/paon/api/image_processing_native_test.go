//go:build !libvips

package api

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gen2brain/avif"
)

func TestNativeImageFallbackResizesAndLogsWarning(t *testing.T) {
	input := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < input.Bounds().Dy(); y++ {
		for x := 0; x < input.Bounds().Dx(); x++ {
			input.Set(x, y, color.NRGBA{R: uint8(x * 20), G: uint8(y * 40), B: 80, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, input); err != nil {
		t.Fatal(err)
	}

	logs := captureFallbackLogs(t)

	output, err := resizeVIPSBufferToMaxPixels(encoded.Bytes(), "image/png", 16)
	if err != nil {
		t.Fatalf("native fallback resize failed: %v", err)
	}
	resized, _, err := image.Decode(bytes.NewReader(output))
	if err != nil {
		t.Fatalf("decode fallback output: %v", err)
	}
	if got := resized.Bounds().Dx() * resized.Bounds().Dy(); got > 16 {
		t.Fatalf("fallback output has %d pixels, want at most 16", got)
	}

	warning := logs.String()
	for _, want := range []string{
		"level=WARN",
		"event=image_processor_fallback",
		`processor="libvips"`,
		`fallback="go-native"`,
		`operation="resize_buffer_to_max_pixels"`,
	} {
		if !strings.Contains(warning, want) {
			t.Fatalf("fallback warning missing %q: %s", want, warning)
		}
	}
}

func TestNativePreviewImageLimitMatchesMastodon43(t *testing.T) {
	if mediaPreviewImageSizeLimit != 2*1024*1024 {
		t.Fatalf("native preview image limit = %d", mediaPreviewImageSizeLimit)
	}
}

func TestNativeImageFallbackConvertsAVIF(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.avif")
	sourceFile, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	input := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	if err := avif.Encode(sourceFile, input); err != nil {
		sourceFile.Close()
		t.Fatalf("encode AVIF fixture: %v", err)
	}
	if err := sourceFile.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "converted.jpeg")
	logs := captureFallbackLogs(t)

	if err := convertVIPSFileToJPEG(source, target); err != nil {
		t.Fatalf("native AVIF fallback conversion failed: %v", err)
	}
	file, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, format, err := image.Decode(file); err != nil || format != "jpeg" {
		t.Fatalf("fallback output format = %q, err = %v; want jpeg", format, err)
	}
	if warning := logs.String(); !strings.Contains(warning, "level=WARN") || !strings.Contains(warning, `operation="convert_file_to_jpeg"`) {
		t.Fatalf("AVIF fallback warning missing WARN operation: %s", warning)
	}
}

func captureFallbackLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})
	return &logs
}
