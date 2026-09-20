# Paon Go media runtime validation

Validation date: 2026-07-11. Image: local `paon-go:integration-check`, image ID `464793d21df9`, built from the Go runtime Dockerfile on arm64. The image predates the standard `Dockerfile` rename but uses the same runtime stages.

The full image build compiled the existing UI assets and installed Debian ffmpeg/ffprobe 5.1.9. A generated odd-dimension H.264 input was processed with Paon's even-dimension crop and `yuv420p` contract; an audio fixture was encoded with the MP3 quality mode used by `railsAudioTranscodeFFmpegArgs`.

| Fixture                                  | Result             | SHA-256                                                            |
| ---------------------------------------- | ------------------ | ------------------------------------------------------------------ |
| 1s `testsrc`, requested 321x241 at 30fps | H.264 MP4, 320x240 | `fb73945f7f3ce7074bd2485d8b88e70dcca64c3bc69cb5648cef5996e088a17d` |
| 1s 1kHz sine                             | MP3                | `69f00f1a8df50a40ed8b5b0472e1fa99f2902ddd6bfc6628edc5831dac1cfa37` |

Command family used inside the production image:

```sh
ffmpeg -f lavfi -i testsrc=size=321x241:rate=30 -t 1 -c:v libx264 -pix_fmt yuv420p -vf 'crop=floor(iw/2)*2:floor(ih/2)*2' video.mp4
ffmpeg -f lavfi -i sine=frequency=1000:duration=1 -q:a 2 audio.mp3
ffprobe -show_entries stream=codec_name,width,height video.mp4
```

This proves the shipped executable/codec baseline and canonical dimensions. It does not replace the remaining release matrix for real user containers/codecs, browser playback, S3-compatible storage ACL/cache behavior, remote-cache modes, malformed/oversized input, and avatar/header visual comparison.

## Mastodon 4.3.23 compatibility matrix

The current container builds against Go 1.25 on Debian trixie, uses the trixie
`ffmpeg`/`ffprobe` package, and links libvips 8.16 when available. A native binary
is built alongside it so a missing system libvips dependency has an explicit,
logged fallback.

| Case                       | Paon 4.3 contract                                                                         | Automated evidence                                                      | External release check                      |
| -------------------------- | ----------------------------------------------------------------------------------------- | ----------------------------------------------------------------------- | ------------------------------------------- |
| More than four attachments | Reject status create/edit while preserving attachment ownership and order                 | `TestValidateStatusMediaAttachmentsMatchesRailsPostAndUpdateValidation` | desktop/mobile compose error copy           |
| Portrait animated GIF      | Keep animated media type and generate bounded preview geometry                            | media type, post-process, and thumbnail tests                           | browser animation/profile layout fixture    |
| Video rotation             | Swap width/height for +/-90 degree ffprobe side data                                      | ffprobe metadata and transcoder-input tests                             | rotated phone-video playback                |
| HEIC/HEIF/AVIF             | Accept, convert the original to JPEG, and generate the normal small style                 | original eligibility, native AVIF, and remote conversion tests          | iOS 18 HEIF in both processor builds        |
| APNG emoji                 | Preserve the PNG original; static style decodes its first image into PNG                  | custom-emoji original/static tests                                      | animated APNG plus static-client fallback   |
| JFIF and Opus              | Map `.jfif` to `image/jpeg`; accept `audio/opus` and transcode audio to MP3               | media content-type and audio transcode tests                            | browser playback and metadata fixture       |
| Passthrough MP4            | Copy compatible streams while relocating `moov` with `-movflags faststart`                | post-process argument and safe in-place replacement tests               | range-request playback before full download |
| Malformed image            | Reject unreadable originals and thumbnails without persisting a usable attachment         | image and thumbnail validation tests                                    | malformed HEIF/APNG corpus                  |
| Processor failure          | Retry asynchronous transcode failure; libvips operations fall back with a bounded warning | processing-transition, command-error, and native fallback tests         | worker retry/dead-letter exercise           |
| User preview size          | 8 MiB with libvips; 2 MiB with native processor                                           | build-tagged limit and native limit test                                | boundary uploads in both production images  |

Local media descriptions remain capped at 1,500 Unicode code points. Federated
descriptions are sanitized and truncated separately at 10,000 code points; the
two limits do not share validation state.
