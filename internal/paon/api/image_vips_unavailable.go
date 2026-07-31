//go:build !libvips

package api

import "fmt"

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
