package api

import "mime"

func init() {
	_ = mime.AddExtensionType(".webp", "image/webp")
}
