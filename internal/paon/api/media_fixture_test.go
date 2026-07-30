package api

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func testWebMFixture(t *testing.T) string {
	t.Helper()
	return generateMediaFixture(t, "attachment.webm", []string{
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=1",
		"-an", "-c:v", "libvpx-vp9", "-pix_fmt", "yuv420p",
	})
}

func testMP4Fixture(t *testing.T) string {
	t.Helper()
	return generateMediaFixture(t, "attachment.mp4", []string{
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=1",
		"-an", "-c:v", "h264", "-pix_fmt", "yuv420p",
	})
}

func testAVIFFixture(t *testing.T) string {
	t.Helper()
	return generateMediaFixture(t, "600x400.avif", []string{
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=1",
		"-frames:v", "1", "-c:v", "libaom-av1", "-still-picture", "1",
	})
}

func generateMediaFixture(t *testing.T, name string, args []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	args = append([]string{"-hide_banner", "-loglevel", "error"}, args...)
	args = append(args, "-y", path)
	if output, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot generate %s fixture: %v: %s", name, err, output)
	}
	return path
}
