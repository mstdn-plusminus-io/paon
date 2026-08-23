package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMediaCommandArgsRestrictEveryInputToLocalProtocols(t *testing.T) {
	const source = "/var/lib/paon/media/input.mp4"
	const target = "/var/lib/paon/media/output.mp4"
	passthroughMetadata := mediaTranscodeMetadata{
		valid:      true,
		videoCodec: "h264",
		audioCodec: "aac",
		colorspace: "yuv420p",
	}

	tests := map[string][]string{
		"video thumbnail": mediaVideoThumbnailFFmpegArgs(source, target),
		"audio thumbnail": mediaAudioThumbnailFFmpegArgs(source, target),
		"metadata probe":  mediaFFprobeArgs(source),
		"video copy":      railsVideoTranscodeFFmpegArgsForMetadata(source, passthroughMetadata),
		"video transcode": railsVideoTranscodeFFmpegArgsForMetadata(source, mediaTranscodeMetadata{}),
		"audio transcode": railsAudioTranscodeFFmpegArgs(source),
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			inputIndex := -1
			for index, arg := range args {
				if arg == "-i" {
					if inputIndex >= 0 {
						t.Fatalf("multiple input options in %#v", args)
					}
					inputIndex = index
				}
			}
			if inputIndex < 2 || inputIndex+1 >= len(args) {
				t.Fatalf("input option missing or malformed in %#v", args)
			}
			if args[inputIndex-2] != "-protocol_whitelist" || args[inputIndex-1] != mediaInputProtocolWhitelist {
				t.Fatalf("protocol whitelist must immediately precede input in %#v", args)
			}
			if args[inputIndex+1] != source {
				t.Fatalf("input = %q, want %q", args[inputIndex+1], source)
			}
		})
	}
}

func TestMediaInputProtocolWhitelistExcludesNetworkTransports(t *testing.T) {
	allowed := map[string]bool{}
	for _, protocol := range strings.Split(mediaInputProtocolWhitelist, ",") {
		allowed[protocol] = true
	}
	for _, protocol := range []string{"file", "pipe", "fd", "crypto", "data"} {
		if !allowed[protocol] {
			t.Errorf("local protocol %q is not allowed", protocol)
		}
	}
	for _, protocol := range []string{"http", "https", "ftp", "ftps", "sftp", "tcp", "tls", "udp", "rtp", "rtmp", "srt", "unix"} {
		if allowed[protocol] {
			t.Errorf("network protocol %q must not be allowed", protocol)
		}
	}
}

func TestFFprobeProtocolWhitelistBlocksNestedHTTPPlaylistInput(t *testing.T) {
	binary, err := exec.LookPath(mediaFFprobeBinary())
	if err != nil {
		t.Skip("ffprobe is not installed")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listener is unavailable: %v", err)
	}
	var requests atomic.Int64
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.NotFound(w, nil)
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	playlist := filepath.Join(t.TempDir(), "network-reference.m3u8")
	playlistBody := fmt.Sprintf(`#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:1
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:1.0,
http://%s/segment.ts
#EXT-X-ENDLIST
`, listener.Addr().String())
	if err := os.WriteFile(playlist, []byte(playlistBody), 0o600); err != nil {
		t.Fatal(err)
	}

	runFFprobe := func(args []string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, binary, args...).CombinedOutput()
	}
	_, _ = runFFprobe([]string{"-v", "error", "-protocol_whitelist", "file,http,tcp", "-i", playlist, "-show_streams"})
	if requests.Load() == 0 {
		t.Fatal("network-reference fixture did not exercise its nested HTTP input")
	}

	requests.Store(0)
	output, err := runFFprobe(mediaFFprobeArgs(playlist))
	if err == nil {
		t.Fatalf("ffprobe unexpectedly accepted a nested HTTP input: %s", output)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("ffprobe made %d HTTP request(s) despite the protocol whitelist", got)
	}
}
