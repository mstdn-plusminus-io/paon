//go:build !libvips

package api

import "fmt"

// The native processor follows Mastodon's ImageMagick-compatible preview
// limit. Original image uploads continue to use IMAGE_SIZE_LIMIT.
const mediaPreviewImageSizeLimit = 2 * 1024 * 1024

const profileImageSizeLimit = 2 * 1024 * 1024

var errVIPSUnavailable = fmt.Errorf("libvips image processor is unavailable in this build")

func tryConvertVIPSFileToJPEG(string, string) error {
	return errVIPSUnavailable
}

func tryResizeVIPSFileToMaxPixels(string, string, string, int) error {
	return errVIPSUnavailable
}

func tryResizeVIPSBufferToMaxPixels([]byte, string, int) ([]byte, error) {
	return nil, errVIPSUnavailable
}

func tryResizeVIPSBufferToFill([]byte, string, int, int) ([]byte, error) {
	return nil, errVIPSUnavailable
}

func tryResizeVIPSFileToFill(string, string, string, int, int) error {
	return errVIPSUnavailable
}

func tryWriteVIPSStaticPNG(string, string) error {
	return errVIPSUnavailable
}
