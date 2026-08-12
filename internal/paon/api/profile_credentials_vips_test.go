//go:build libvips

package api

import "testing"

func TestMastodon44LibvipsProfileImageLimitIsEightMiB(t *testing.T) {
	if profileImageSizeLimit != 8*1024*1024 {
		t.Fatalf("profileImageSizeLimit = %d, want 8 MiB with libvips", profileImageSizeLimit)
	}
}
