# Paon Go media runtime validation

Validation date: 2026-07-11. Image: local `paon-go:integration-check`, image ID `464793d21df9`, built from the Go runtime Dockerfile on arm64. The image predates the standard `Dockerfile` rename but uses the same runtime stages.

The full image build compiled the existing UI assets and installed Debian ffmpeg/ffprobe 5.1.9. A generated odd-dimension H.264 input was processed with Paon's even-dimension crop and `yuv420p` contract; an audio fixture was encoded with the MP3 quality mode used by `railsAudioTranscodeFFmpegArgs`.

| Fixture | Result | SHA-256 |
|---|---|---|
| 1s `testsrc`, requested 321x241 at 30fps | H.264 MP4, 320x240 | `fb73945f7f3ce7074bd2485d8b88e70dcca64c3bc69cb5648cef5996e088a17d` |
| 1s 1kHz sine | MP3 | `69f00f1a8df50a40ed8b5b0472e1fa99f2902ddd6bfc6628edc5831dac1cfa37` |

Command family used inside the production image:

```sh
ffmpeg -f lavfi -i testsrc=size=321x241:rate=30 -t 1 -c:v libx264 -pix_fmt yuv420p -vf 'crop=floor(iw/2)*2:floor(ih/2)*2' video.mp4
ffmpeg -f lavfi -i sine=frequency=1000:duration=1 -q:a 2 audio.mp3
ffprobe -show_entries stream=codec_name,width,height video.mp4
```

This proves the shipped executable/codec baseline and canonical dimensions. It does not replace the remaining release matrix for real user containers/codecs, browser playback, S3-compatible storage ACL/cache behavior, remote-cache modes, malformed/oversized input, and avatar/header visual comparison.
