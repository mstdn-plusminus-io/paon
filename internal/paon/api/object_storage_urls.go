package api

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const azureBlobSASVersion = "2020-10-02"

func (s *Server) presignedSwiftObjectURL(key string, expires time.Duration) string {
	if s == nil || !s.cfg.SwiftEnabled || strings.TrimSpace(s.cfg.SwiftTempURLKey) == "" || strings.TrimSpace(s.cfg.SwiftObjectURL) == "" {
		return ""
	}
	if expires <= 0 {
		expires = time.Hour
	}
	parsed, err := url.Parse(s.cfg.SwiftObjectURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Path = "/" + path.Join(swiftObjectURLPathPrefix(parsed.EscapedPath(), s.cfg.SwiftContainer), strings.Trim(key, "/"))
	parsed.RawPath = ""
	expiresAt := time.Now().UTC().Add(expires).Unix()
	mac := hmac.New(sha1.New, []byte(s.cfg.SwiftTempURLKey))
	_, _ = mac.Write([]byte("GET\n" + strconv.FormatInt(expiresAt, 10) + "\n" + parsed.EscapedPath()))
	query := parsed.Query()
	query.Set("temp_url_sig", hex.EncodeToString(mac.Sum(nil)))
	query.Set("temp_url_expires", strconv.FormatInt(expiresAt, 10))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func swiftObjectURLPathPrefix(rawPath string, container string) string {
	prefix := strings.Trim(rawPath, "/")
	container = strings.Trim(container, "/")
	if container == "" {
		return prefix
	}
	segments := strings.Split(prefix, "/")
	if len(segments) > 0 && segments[len(segments)-1] == container {
		return prefix
	}
	return path.Join(prefix, container)
}

func (s *Server) presignedAzureBlobURL(key string, expires time.Duration) string {
	if s == nil || !s.cfg.AzureEnabled || strings.TrimSpace(s.cfg.AzureStorageAccount) == "" || strings.TrimSpace(s.cfg.AzureStorageAccessKey) == "" || strings.TrimSpace(s.cfg.AzureContainerName) == "" {
		return ""
	}
	if expires <= 0 {
		expires = time.Hour
	}
	baseURL := s.azureBlobBaseURL()
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	container := strings.Trim(s.cfg.AzureContainerName, "/")
	parsed.Path = "/" + path.Join(strings.Trim(parsed.EscapedPath(), "/"), container, strings.Trim(key, "/"))
	parsed.RawPath = ""

	expiresAt := time.Now().UTC().Add(expires).Format("2006-01-02T15:04:05Z")
	canonicalizedResource := "/blob/" + s.cfg.AzureStorageAccount + "/" + path.Join(container, strings.Trim(key, "/"))
	stringToSign := strings.Join([]string{
		"r",
		"",
		expiresAt,
		canonicalizedResource,
		"",
		"",
		"https",
		azureBlobSASVersion,
		"b",
		"",
		"",
		"",
		"",
		"",
		"",
	}, "\n")
	keyBytes, err := base64.StdEncoding.DecodeString(s.cfg.AzureStorageAccessKey)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, keyBytes)
	_, _ = mac.Write([]byte(stringToSign))
	query := parsed.Query()
	query.Set("sv", azureBlobSASVersion)
	query.Set("spr", "https")
	query.Set("se", expiresAt)
	query.Set("sr", "b")
	query.Set("sp", "r")
	query.Set("sig", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *Server) azureBlobBaseURL() string {
	if alias := strings.TrimSpace(s.cfg.AzureAliasHost); alias != "" {
		return normalizeExternalURL(alias)
	}
	account := strings.TrimSpace(s.cfg.AzureStorageAccount)
	if account == "" {
		return ""
	}
	return "https://" + account + ".blob.core.windows.net"
}

func normalizeExternalURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "https://" + raw
}
