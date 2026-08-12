package api

import (
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

// mediaPrivacyStaticHandler keeps the monolithic Go file server from bypassing
// the private filesystem mode used for media retained with moderated posts.
// In upstream deployments nginx cannot read the 0600 file; Paon serves static
// files in-process, so it must enforce the same boundary itself.
func (s *Server) mediaPrivacyStaticHandler(fileSystem fs.FS) echo.HandlerFunc {
	return func(c *echo.Context) error {
		candidates := staticAssetPathCandidates(c.Param("*"))
		if len(candidates) == 0 {
			return echo.ErrNotFound
		}
		for _, name := range candidates {
			if name == "." || name == ".." || strings.HasPrefix(name, "../") {
				return echo.ErrNotFound
			}
			attachmentID, mediaAsset := mediaAttachmentStaticAssetID(name)
			if !mediaAsset {
				continue
			}
			info, err := fs.Stat(fileSystem, name)
			if err != nil {
				return echo.ErrNotFound
			}
			if info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0 {
				if !s.privateMediaAttachmentMayBeServed(c, attachmentID) {
					return echo.ErrNotFound
				}
			}
		}
		file, err := fileSystem.Open(candidates[0])
		if err != nil {
			return echo.ErrNotFound
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.IsDir() {
			return echo.ErrNotFound
		}
		seeker, ok := file.(io.ReadSeeker)
		if !ok {
			return echo.ErrNotFound
		}
		http.ServeContent(c.Response(), c.Request(), info.Name(), info.ModTime(), seeker)
		return nil
	}
}

func staticAssetPathCandidates(raw string) []string {
	normalized := path.Clean(strings.TrimPrefix(raw, "/"))
	out := []string{normalized}
	if unescaped, err := url.PathUnescape(raw); err == nil {
		candidate := path.Clean(strings.TrimPrefix(unescaped, "/"))
		if candidate != normalized {
			out = append(out, candidate)
		}
	}
	return out
}

func isMediaAttachmentStaticAsset(name string) bool {
	_, ok := mediaAttachmentStaticAssetID(name)
	return ok
}

func mediaAttachmentStaticAssetID(name string) (int64, bool) {
	parts := strings.Split(path.Clean(name), "/")
	if len(parts) > 0 && parts[0] == "cache" {
		parts = parts[1:]
	}
	if len(parts) < 6 || parts[0] != "media_attachments" || (parts[1] != "files" && parts[1] != "thumbnails") {
		return 0, false
	}
	partition := strings.Join(parts[2:len(parts)-2], "")
	id, err := strconv.ParseInt(partition, 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) privateMediaAttachmentMayBeServed(c *echo.Context, attachmentID int64) bool {
	if s == nil || s.db == nil || c == nil || attachmentID == 0 {
		return false
	}
	var attachment models.MediaAttachment
	if err := s.db.WithContext(c.Request().Context()).
		Preload("Status.Account").
		Where("id = ?", attachmentID).
		First(&attachment).Error; err != nil {
		return false
	}
	if attachment.StatusID.Valid {
		if attachment.Status.ID == 0 || attachment.Status.DeletedAt.Valid || attachment.Status.Account.SuspendedAt.Valid {
			return false
		}
	}
	rejected, err := s.proxyMediaRejectedByAccountDomain(attachment)
	return err == nil && !rejected
}
