package api

import (
	"bytes"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/gen2brain/avif"
	_ "github.com/perkeep/heic"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const vipsJPEGQuality = 90

func convertVIPSFileToJPEG(source string, target string) error {
	if err := tryConvertVIPSFileToJPEG(source, target); err == nil {
		return nil
	} else {
		warnImageProcessorFallback("convert_file_to_jpeg", err)
	}
	return nativeConvertFileToJPEG(source, target)
}

func resizeVIPSFileToMaxPixels(source string, target string, contentType string, maxPixels int) error {
	if err := tryResizeVIPSFileToMaxPixels(source, target, contentType, maxPixels); err == nil {
		return nil
	} else {
		warnImageProcessorFallback("resize_file_to_max_pixels", err)
	}
	return nativeResizeFileToMaxPixels(source, target, contentType, maxPixels)
}

func resizeVIPSBufferToMaxPixels(data []byte, contentType string, maxPixels int) ([]byte, error) {
	if output, err := tryResizeVIPSBufferToMaxPixels(data, contentType, maxPixels); err == nil {
		return output, nil
	} else {
		warnImageProcessorFallback("resize_buffer_to_max_pixels", err)
	}
	return nativeResizeBufferToMaxPixels(data, contentType, maxPixels)
}

func resizeVIPSBufferToFill(data []byte, contentType string, width int, height int) ([]byte, error) {
	if output, err := tryResizeVIPSBufferToFill(data, contentType, width, height); err == nil {
		return output, nil
	} else {
		warnImageProcessorFallback("resize_buffer_to_fill", err)
	}
	return nativeResizeBufferToFill(data, contentType, width, height)
}

func resizeVIPSFileToFill(source string, target string, contentType string, width int, height int) error {
	if err := tryResizeVIPSFileToFill(source, target, contentType, width, height); err == nil {
		return nil
	} else {
		warnImageProcessorFallback("resize_file_to_fill", err)
	}
	return nativeResizeFileToFill(source, target, contentType, width, height)
}

func writeVIPSStaticPNG(source string, target string) error {
	if err := tryWriteVIPSStaticPNG(source, target); err == nil {
		return nil
	} else {
		warnImageProcessorFallback("write_static_png", err)
	}
	return nativeWriteStaticPNG(source, target)
}

func warnImageProcessorFallback(operation string, err error) {
	log.Printf("level=WARN event=image_processor_fallback processor=%q fallback=%q operation=%q error=%q", "libvips", "go-native", operation, err)
}

func nativeConvertFileToJPEG(source string, target string) error {
	img, err := decodeNativeImageFile(source)
	if err != nil {
		return fmt.Errorf("decode image with Go native processor: %w", err)
	}
	return writeNativeImageFile(target, "image/jpeg", img)
}

func nativeResizeFileToMaxPixels(source string, target string, contentType string, maxPixels int) error {
	img, err := decodeNativeImageFile(source)
	if err != nil {
		return fmt.Errorf("decode image with Go native processor: %w", err)
	}
	return writeNativeImageFile(target, contentType, nativeResizeImageToMaxPixels(img, maxPixels))
}

func nativeResizeBufferToMaxPixels(data []byte, contentType string, maxPixels int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image with Go native processor: %w", err)
	}
	return encodeNativeImage(contentType, nativeResizeImageToMaxPixels(img, maxPixels))
}

func nativeResizeBufferToFill(data []byte, contentType string, width int, height int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image with Go native processor: %w", err)
	}
	resized, err := nativeResizeImageToFill(img, width, height)
	if err != nil {
		return nil, err
	}
	return encodeNativeImage(contentType, resized)
}

func nativeResizeFileToFill(source string, target string, contentType string, width int, height int) error {
	img, err := decodeNativeImageFile(source)
	if err != nil {
		return fmt.Errorf("decode image with Go native processor: %w", err)
	}
	resized, err := nativeResizeImageToFill(img, width, height)
	if err != nil {
		return err
	}
	return writeNativeImageFile(target, contentType, resized)
}

func nativeWriteStaticPNG(source string, target string) error {
	img, err := decodeNativeImageFile(source)
	if err != nil {
		return fmt.Errorf("decode static image with Go native processor: %w", err)
	}
	return writeNativeImageFile(target, "image/png", img)
}

func decodeNativeImageFile(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	return img, err
}

func nativeResizeImageToMaxPixels(source image.Image, maxPixels int) image.Image {
	bounds := source.Bounds()
	width, height := thumbnailDimensions(bounds.Dx(), bounds.Dy(), maxPixels)
	if width == bounds.Dx() && height == bounds.Dy() && bounds.Min.X == 0 && bounds.Min.Y == 0 {
		return source
	}
	target := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(target, target.Bounds(), source, bounds, draw.Over, nil)
	return target
}

func nativeResizeImageToFill(source image.Image, width int, height int) (image.Image, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid Go native target geometry %dx%d", width, height)
	}
	bounds := source.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil, fmt.Errorf("invalid Go native source geometry %dx%d", bounds.Dx(), bounds.Dy())
	}
	scale := math.Max(float64(width)/float64(bounds.Dx()), float64(height)/float64(bounds.Dy()))
	scaledWidth := int(math.Ceil(float64(bounds.Dx()) * scale))
	scaledHeight := int(math.Ceil(float64(bounds.Dy()) * scale))
	scaled := image.NewNRGBA(image.Rect(0, 0, scaledWidth, scaledHeight))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), source, bounds, draw.Over, nil)
	offsetX := (scaledWidth - width) / 2
	offsetY := (scaledHeight - height) / 2
	target := image.NewNRGBA(image.Rect(0, 0, width, height))
	draw.Copy(target, image.Point{}, scaled, image.Rect(offsetX, offsetY, offsetX+width, offsetY+height), draw.Src, nil)
	return target, nil
}

func writeNativeImageFile(target string, contentType string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	file, err := os.Create(target)
	if err != nil {
		return err
	}
	encodeErr := encodeNativeImageTo(file, contentType, img)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}

func encodeNativeImage(contentType string, img image.Image) ([]byte, error) {
	var output bytes.Buffer
	if err := encodeNativeImageTo(&output, contentType, img); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func encodeNativeImageTo(target interface{ Write([]byte) (int, error) }, contentType string, img image.Image) error {
	switch normalizedImageContentType(contentType) {
	case "image/jpeg":
		return jpeg.Encode(target, img, &jpeg.Options{Quality: vipsJPEGQuality})
	case "image/png":
		return png.Encode(target, img)
	case "image/gif":
		return gif.Encode(target, img, nil)
	default:
		return fmt.Errorf("unsupported Go native output content type %q", contentType)
	}
}

func normalizedImageContentType(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
}
