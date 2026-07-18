package api

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

// remoteAccountImageLimit mirrors Rails AccountAvatar::LIMIT / AccountHeader::LIMIT (2 MiB).
const remoteAccountImageLimit = 2 * 1024 * 1024

// remoteAccountAvatarTarget is the avatar fill-crop geometry (Rails avatar style 400x400#).
const remoteAccountAvatarTarget = 400

// remoteAccountHeaderMaxPixels mirrors Rails AccountHeader::MAX_PIXELS (1500x500px).
const remoteAccountHeaderMaxPixels = 750000

// downloadAndStoreRemoteAccountImage mirrors Rails Remotable#download_avatar!/download_header!
// (invoked by the avatar_remote_url=/header_remote_url= setters via download_on_assign): it
// fetches the remote image, resizes it to the Rails avatar/header style, stores the original +
// static styles, and updates the account's avatar_*/header_* columns. Returns an error on
// fetch/decode failure so callers can enqueue a delayed redownload retry
// (Rails RedownloadAvatarWorker / RedownloadHeaderWorker).
func (s *Server) downloadAndStoreRemoteAccountImage(ctx context.Context, accountID int64, kind string, remoteURL string) error {
	if s == nil || s.db == nil || accountID == 0 || strings.TrimSpace(remoteURL) == "" {
		return nil
	}
	if s.cfg.DisableRemoteMediaCache {
		return nil
	}
	download, err := fetchRemoteImageMedia(ctx, remoteURL, remoteAccountImageLimit)
	if err != nil {
		return err
	}
	contentType := download.contentType
	filename := download.filename
	if !profileImageContentTypeSupported(contentType) {
		return fmt.Errorf("remote account %s has unsupported content type %q", kind, contentType)
	}
	img, _, err := image.Decode(bytes.NewReader(download.body))
	if err != nil {
		return err
	}
	resized := resizeRemoteAccountImage(kind, img)
	if resized == nil {
		return fmt.Errorf("unknown account image kind %q", kind)
	}
	// Go cannot re-encode webp; normalize to png so the stored file matches its content type and
	// filename extension (avoids a content_type=webp file whose bytes are png).
	if strings.EqualFold(strings.TrimSpace(contentType), "image/webp") {
		contentType = "image/png"
		if ext := filepath.Ext(filename); ext != "" {
			filename = strings.TrimSuffix(filename, ext) + ".png"
		} else {
			filename += ".png"
		}
	}
	data, err := encodeAccountImage(resized, contentType)
	if err != nil {
		return err
	}
	if err := s.storeAccountImageBytes(accountID, kind, filename, contentType, data); err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&models.Account{}).Where("id = ?", accountID).Updates(profileImageUpdates(kind, filename, contentType, int64(len(data)), now)).Error
}

// resizeRemoteAccountImage applies the Rails avatar (400x400# fill crop) or header
// (<=750000px²) style to the decoded image. Returns nil for an unknown kind.
func resizeRemoteAccountImage(kind string, img image.Image) image.Image {
	switch kind {
	case "avatar":
		return resizeImageToFill(img, remoteAccountAvatarTarget, remoteAccountAvatarTarget)
	case "header":
		return resizeImageToMaxPixels(img, remoteAccountHeaderMaxPixels)
	default:
		return nil
	}
}

// encodeAccountImage encodes the image in its original format. webp callers must normalize to
// png before calling (see downloadAndStoreRemoteAccountImage).
func encodeAccountImage(img image.Image, contentType string) ([]byte, error) {
	var buf bytes.Buffer
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/jpeg":
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			return nil, err
		}
	case "image/gif":
		if err := gif.Encode(&buf, img, nil); err != nil {
			return nil, err
		}
	case "image/png":
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
	default:
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

// storeAccountImageBytes is the []byte variant of storeAccountImageFile
// (profile_credentials.go): it writes the original style locally + S3 and writes the
// static style through the shared Rails-style profile image pipeline.
func (s *Server) storeAccountImageBytes(accountID int64, kind string, filename string, contentType string, data []byte) error {
	target := s.accountImagePath(accountID, kind, filename)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return err
	}
	ctx := context.Background()
	if err := s.uploadPaperclipObject(ctx, accountImageObjectKey(accountID, kind, "original", filename), target, contentType); err != nil {
		return err
	}
	return s.storeAccountImageStaticStyle(accountID, kind, filename, target, contentType)
}
