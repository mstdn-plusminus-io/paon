//go:build libvips

package api

import (
	"fmt"
	"os"
	"path/filepath"

	vips "github.com/cshum/vipsgen/vips816"
)

// Mastodon 4.3 permits larger user-provided preview images when libvips is
// available. This limit is deliberately separate from the original media
// attachment size limit.
const mediaPreviewImageSizeLimit = 8 * 1024 * 1024

// Mastodon 4.4 raises local avatar and header uploads to 8 MiB when libvips
// is the active processor. Native/ImageMagick-compatible builds retain the
// 2 MiB contract declared in image_vips_unavailable.go.
const profileImageSizeLimit = 8 * 1024 * 1024

func tryConvertVIPSFileToJPEG(source string, target string) error {
	img, err := vips.NewImageFromFile(source, vips.DefaultLoadOptions())
	if err != nil {
		return fmt.Errorf("load image with libvips: %w", err)
	}
	defer img.Close()
	return saveVIPSImageFile(img, target, "image/jpeg")
}

func tryResizeVIPSFileToMaxPixels(source string, target string, contentType string, maxPixels int) error {
	probe, err := vips.NewImageFromFile(source, vips.DefaultLoadOptions())
	if err != nil {
		return fmt.Errorf("load image dimensions with libvips: %w", err)
	}
	width, height := thumbnailDimensions(probe.Width(), probe.Height(), maxPixels)
	probe.Close()

	options := vips.DefaultThumbnailOptions()
	options.Height = height
	options.Size = vips.SizeDown
	options.NoRotate = true
	options.FailOn = vips.FailOnError
	img, err := vips.NewThumbnail(source, width, options)
	if err != nil {
		return fmt.Errorf("resize image with libvips: %w", err)
	}
	defer img.Close()
	return saveVIPSImageFile(img, target, contentType)
}

func tryResizeVIPSBufferToMaxPixels(data []byte, contentType string, maxPixels int) ([]byte, error) {
	probe, err := vips.NewImageFromBuffer(data, vips.DefaultLoadOptions())
	if err != nil {
		return nil, fmt.Errorf("load image dimensions with libvips: %w", err)
	}
	width, height := thumbnailDimensions(probe.Width(), probe.Height(), maxPixels)
	probe.Close()

	options := vips.DefaultThumbnailBufferOptions()
	options.Height = height
	options.Size = vips.SizeDown
	options.NoRotate = true
	options.FailOn = vips.FailOnError
	img, err := vips.NewThumbnailBuffer(data, width, options)
	if err != nil {
		return nil, fmt.Errorf("resize image with libvips: %w", err)
	}
	defer img.Close()
	return encodeVIPSImage(img, contentType)
}

func tryResizeVIPSBufferToFill(data []byte, contentType string, width int, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid libvips target geometry %dx%d", width, height)
	}
	options := vips.DefaultThumbnailBufferOptions()
	options.Height = height
	options.Size = vips.SizeBoth
	options.NoRotate = true
	options.Crop = vips.InterestingCentre
	options.FailOn = vips.FailOnError
	img, err := vips.NewThumbnailBuffer(data, width, options)
	if err != nil {
		return nil, fmt.Errorf("resize and crop image with libvips: %w", err)
	}
	defer img.Close()
	return encodeVIPSImage(img, contentType)
}

func tryResizeVIPSFileToFill(source string, target string, contentType string, width int, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid libvips target geometry %dx%d", width, height)
	}
	options := vips.DefaultThumbnailOptions()
	options.Height = height
	options.Size = vips.SizeBoth
	options.NoRotate = true
	options.Crop = vips.InterestingCentre
	options.FailOn = vips.FailOnError
	img, err := vips.NewThumbnail(source, width, options)
	if err != nil {
		return fmt.Errorf("resize and crop image with libvips: %w", err)
	}
	defer img.Close()
	return saveVIPSImageFile(img, target, contentType)
}

func tryWriteVIPSStaticPNG(source string, target string) error {
	img, err := vips.NewImageFromFile(source, vips.DefaultLoadOptions())
	if err != nil {
		return fmt.Errorf("load static image with libvips: %w", err)
	}
	defer img.Close()
	return saveVIPSImageFile(img, target, "image/png")
}

func saveVIPSImageFile(img *vips.Image, target string, contentType string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	var err error
	switch normalizedImageContentType(contentType) {
	case "image/jpeg":
		options := vips.DefaultJpegsaveOptions()
		options.Q = vipsJPEGQuality
		err = img.Jpegsave(target, options)
	case "image/png":
		err = img.Pngsave(target, vips.DefaultPngsaveOptions())
	case "image/gif":
		err = img.Gifsave(target, vips.DefaultGifsaveOptions())
	case "image/webp":
		err = img.Webpsave(target, vips.DefaultWebpsaveOptions())
	default:
		return fmt.Errorf("unsupported libvips output content type %q", contentType)
	}
	if err != nil {
		return fmt.Errorf("save image with libvips: %w", err)
	}
	return nil
}

func encodeVIPSImage(img *vips.Image, contentType string) ([]byte, error) {
	var (
		data []byte
		err  error
	)
	switch normalizedImageContentType(contentType) {
	case "image/jpeg":
		options := vips.DefaultJpegsaveBufferOptions()
		options.Q = vipsJPEGQuality
		data, err = img.JpegsaveBuffer(options)
	case "image/png":
		data, err = img.PngsaveBuffer(vips.DefaultPngsaveBufferOptions())
	case "image/gif":
		data, err = img.GifsaveBuffer(vips.DefaultGifsaveBufferOptions())
	case "image/webp":
		data, err = img.WebpsaveBuffer(vips.DefaultWebpsaveBufferOptions())
	default:
		return nil, fmt.Errorf("unsupported libvips output content type %q", contentType)
	}
	if err != nil {
		return nil, fmt.Errorf("encode image with libvips: %w", err)
	}
	return data, nil
}
