package api

// mediaInputProtocolWhitelist mirrors Mastodon's network-disabled FFmpeg build
// for Paon's distribution-provided ffmpeg and ffprobe binaries. Paon passes a
// local file as every direct input. The remaining protocols preserve local
// nested resources and standard stream/fd wrappers without permitting network
// transports such as HTTP, FTP, TCP, or UDP.
const mediaInputProtocolWhitelist = "file,pipe,fd,crypto,data"

func mediaLocalInputArgs(path string) []string {
	return []string{"-protocol_whitelist", mediaInputProtocolWhitelist, "-i", path}
}

func mediaVideoThumbnailFFmpegArgs(source string, target string) []string {
	args := append([]string{"-y"}, mediaLocalInputArgs(source)...)
	return append(args, "-frames:v", "1", "-vf", "thumbnail,scale=480:-2", target)
}

func mediaAudioThumbnailFFmpegArgs(source string, target string) []string {
	args := append([]string{"-y"}, mediaLocalInputArgs(source)...)
	return append(args, "-loglevel", "fatal", target)
}

func mediaFFprobeArgs(path string) []string {
	args := mediaLocalInputArgs(path)
	return append(args, "-print_format", "json", "-show_format", "-show_streams", "-show_error", "-loglevel", "fatal")
}
