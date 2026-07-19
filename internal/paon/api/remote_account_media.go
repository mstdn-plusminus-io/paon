package api

import (
	"context"
	"fmt"
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
	// Keep the existing Paperclip-compatible WebP-to-PNG storage contract now that libvips owns
	// decoding, resizing, and encoding.
	if strings.EqualFold(strings.TrimSpace(contentType), "image/webp") {
		contentType = "image/png"
		if ext := filepath.Ext(filename); ext != "" {
			filename = strings.TrimSuffix(filename, ext) + ".png"
		} else {
			filename += ".png"
		}
	}
	data, err := resizeAccountImageBuffer(kind, download.body, contentType)
	if err != nil {
		return err
	}
	if err := s.storeAccountImageBytes(accountID, kind, filename, contentType, data); err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&models.Account{}).Where("id = ?", accountID).Updates(profileImageUpdates(kind, filename, contentType, int64(len(data)), now)).Error
}

// resizeAccountImageBuffer applies the Rails avatar (400x400# fill crop) or header
// (<=750000px²) style through libvips.
func resizeAccountImageBuffer(kind string, data []byte, contentType string) ([]byte, error) {
	switch kind {
	case "avatar":
		return resizeVIPSBufferToFill(data, contentType, remoteAccountAvatarTarget, remoteAccountAvatarTarget)
	case "header":
		return resizeVIPSBufferToMaxPixels(data, contentType, remoteAccountHeaderMaxPixels)
	default:
		return nil, fmt.Errorf("unknown account image kind %q", kind)
	}
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
