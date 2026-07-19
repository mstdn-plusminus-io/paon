package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const cacheBusterHTTPTimeout = 10 * time.Second

var cacheBusterHTTPClient = &http.Client{Timeout: cacheBusterHTTPTimeout}

func (s *Server) bustMediaAttachmentCache(attachment models.MediaAttachment) {
	if s == nil || !s.cfg.CacheBusterEnabled || attachment.ID == 0 {
		return
	}
	for _, object := range s.mediaAttachmentStoredObjects(attachment) {
		s.bustCacheURL(s.cacheBusterMediaAttachmentURL(attachment.ID, object.attachment, object.style, object.filename))
	}
}

func (s *Server) bustAccountImageCache(account models.Account) {
	if s == nil || !s.cfg.CacheBusterEnabled || account.ID == 0 {
		return
	}
	if account.AvatarFileName.Valid && strings.TrimSpace(account.AvatarFileName.String) != "" {
		s.bustAccountImageKindCache(account.ID, "avatar", account.AvatarFileName.String, account.AvatarContentType.String)
	}
	if account.HeaderFileName.Valid && strings.TrimSpace(account.HeaderFileName.String) != "" {
		s.bustAccountImageKindCache(account.ID, "header", account.HeaderFileName.String, account.HeaderContentType.String)
	}
}

func (s *Server) bustAccountImageKindCache(accountID int64, kind string, filename string, contentType string) {
	s.bustCacheURL(s.cacheBusterAccountImageURL(accountID, kind, "original", filename))
	if static := profileImageStaticFilename(filename, contentType); static != "" {
		s.bustCacheURL(s.cacheBusterAccountImageURL(accountID, kind, "static", static))
	}
}

func (s *Server) bustCacheURL(rawURL string) {
	if s == nil {
		return
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return
	}
	if s.enqueueCacheBusterTask(rawURL) {
		return
	}
	s.bustCacheURLNow(rawURL)
}

func (s *Server) bustCacheURLNow(rawURL string) {
	if s == nil {
		return
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	method := s.cfg.CacheBusterHTTPMethod
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, http.NoBody)
	if err != nil {
		return
	}
	if header := strings.TrimSpace(s.cfg.CacheBusterSecretHeader); header != "" {
		if secret := s.cfg.CacheBusterSecret; secret != "" {
			req.Header.Set(header, secret)
		}
	}
	resp, err := cacheBusterHTTPClient.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func (s *Server) cacheBusterMediaAttachmentURL(id int64, attachment string, style string, filename string) string {
	path := "media_attachments/" + attachment + "/" + strings.ReplaceAll(mediaPaperclipIDPartition(id), "\\", "/") + "/" + style + "/" + url.PathEscape(filename)
	return s.cacheBusterAssetURL(path)
}

func (s *Server) cacheBusterAccountImageURL(id int64, kind string, style string, filename string) string {
	dir := "avatars"
	if kind == "header" {
		dir = "headers"
	}
	path := "accounts/" + dir + "/" + strings.ReplaceAll(mediaPaperclipIDPartition(id), "\\", "/") + "/" + style + "/" + url.PathEscape(filename)
	return s.cacheBusterAssetURL(path)
}

func (s *Server) cacheBusterAssetURL(path string) string {
	path = strings.TrimLeft(strings.TrimSpace(path), "/")
	if path == "" {
		return ""
	}
	if strings.TrimSpace(s.cfg.StorageHost) != "" {
		return s.cfg.SystemAssetURL(path)
	}
	root := strings.TrimRight(strings.TrimSpace(s.cfg.PaperclipRootURL), "/")
	if root == "" {
		root = "/system"
	}
	if strings.HasPrefix(root, "http://") || strings.HasPrefix(root, "https://") {
		return root + "/" + path
	}
	base := strings.TrimRight(strings.TrimSpace(s.cfg.CDNHost), "/")
	if base == "" {
		base = s.cfg.BaseURL()
	}
	root = strings.Trim(root, "/")
	if root == "" {
		return base + "/" + path
	}
	return base + "/" + root + "/" + path
}
