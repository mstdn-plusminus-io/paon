package api

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlurhashEncodeBase83(t *testing.T) {
	if got := blurhashEncodeBase83(0, 1); got != "0" {
		t.Fatalf("base83 zero = %q", got)
	}
	if got := blurhashEncodeBase83(83, 2); got != "10" {
		t.Fatalf("base83 83 = %q", got)
	}
}

func TestBlurhashEncodeUsesMastodonComponentCount(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: uint8(20 + x*20), G: uint8(10 + y*20), B: 120, A: 255})
		}
	}
	hash, ok := blurhashEncode(img, 4, 4)
	if !ok {
		t.Fatal("blurhash encode failed")
	}
	if len(hash) != 36 {
		t.Fatalf("blurhash length = %d, want 36: %q", len(hash), hash)
	}
	for _, r := range hash {
		if !strings.ContainsRune(blurhashAlphabet, r) {
			t.Fatalf("blurhash contains invalid character %q in %q", r, hash)
		}
	}
	if again, _ := blurhashEncode(img, 4, 4); again != hash {
		t.Fatalf("blurhash is not deterministic: %q != %q", again, hash)
	}
}

func TestBlurhashForStoredImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preview.png")
	img := image.NewRGBA(image.Rect(0, 0, 12, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 12; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 10), G: uint8(y * 12), B: 180, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, img); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	hash := blurhashForStoredImage(path)
	if len(hash) != 36 {
		t.Fatalf("blurhash = %q", hash)
	}
	if got := blurhashForStoredImage(filepath.Join(t.TempDir(), "missing.png")); got != "" {
		t.Fatalf("missing image blurhash = %q", got)
	}
}
