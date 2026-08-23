package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"golang.org/x/crypto/pbkdf2"
)

const asyncRefreshVerifierPurpose = "async_refreshes"

type asyncRefreshEntity struct {
	AsyncRefresh struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		ResultCount *int64 `json:"result_count"`
	} `json:"async_refresh"`
}

func (s *Server) showAsyncRefresh(c *echo.Context) error {
	if _, _, err := s.requireUserScope(c, "read"); err != nil {
		return err
	}
	key, ok := asyncRefreshKeyFromID(c.Param("id"), s.cfg.SecretKeyBase)
	if !ok {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	statusValue, err := s.redisCommand(c.Request().Context(), "HGET", key, "status")
	if err != nil {
		return err
	}
	status, ok := statusValue.(string)
	if !ok || strings.TrimSpace(status) == "" {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	var resultCount *int64
	countValue, err := s.redisCommand(c.Request().Context(), "HGET", key, "result_count")
	if err != nil {
		return err
	}
	if raw, ok := countValue.(string); ok && strings.TrimSpace(raw) != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			resultCount = &parsed
		}
	}
	entity := asyncRefreshEntity{}
	entity.AsyncRefresh.ID = c.Param("id")
	entity.AsyncRefresh.Status = status
	entity.AsyncRefresh.ResultCount = resultCount
	return c.JSON(http.StatusOK, entity)
}

func asyncRefreshID(key string, secret string) (string, bool) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(secret) == "" {
		return "", false
	}
	payload, err := json.Marshal(key)
	if err != nil {
		return "", false
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	return encoded + "--" + hex.EncodeToString(asyncRefreshSignature(encoded, secret)), true
}

func asyncRefreshKeyFromID(id string, secret string) (string, bool) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(secret) == "" {
		return "", false
	}
	separator := strings.LastIndex(id, "--")
	if separator <= 0 {
		return "", false
	}
	encoded := id[:separator]
	digest, err := hex.DecodeString(id[separator+2:])
	if err != nil || len(digest) != sha1.Size || !hmac.Equal(digest, asyncRefreshSignature(encoded, secret)) {
		return "", false
	}
	payload, err := decodeSelfDestructMessage(encoded)
	if err != nil {
		return "", false
	}
	var key string
	if json.Unmarshal(payload, &key) != nil || strings.TrimSpace(key) == "" {
		return "", false
	}
	return key, true
}

func asyncRefreshSignature(encoded string, secret string) []byte {
	key := pbkdf2.Key([]byte(secret), []byte(asyncRefreshVerifierPurpose), 1000, 64, sha256.New)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write([]byte(encoded))
	return mac.Sum(nil)
}

func (s *Server) createAsyncRefresh(ctx context.Context, key string, countResults bool) (string, error) {
	args := []string{"HSET", key, "status", "running"}
	if countResults {
		args = append(args, "result_count", "0")
	}
	if _, err := s.redisCommand(ctx, args...); err != nil {
		return "", err
	}
	if _, err := s.redisCommand(ctx, "EXPIRE", key, "86400"); err != nil {
		return "", err
	}
	id, ok := asyncRefreshID(key, s.cfg.SecretKeyBase)
	if !ok {
		return "", apiHTTPError{status: http.StatusInternalServerError, message: "Async refresh cannot be signed"}
	}
	return id, nil
}

func (s *Server) finishAsyncRefresh(ctx context.Context, key string) error {
	if _, err := s.redisCommand(ctx, "HSET", key, "status", "finished"); err != nil {
		return err
	}
	_, err := s.redisCommand(ctx, "EXPIRE", key, "3600")
	return err
}

func (s *Server) incrementAsyncRefreshResult(ctx context.Context, key string, by int64) error {
	_, err := s.redisCommand(ctx, "HINCRBY", key, "result_count", strconv.FormatInt(by, 10))
	return err
}

func (s *Server) addAsyncRefreshPendingJob(ctx context.Context, key string) error {
	_, err := s.redisCommand(ctx, "HINCRBY", key, "pending_jobs", "1")
	return err
}

func (s *Server) completeAsyncRefreshJob(ctx context.Context, key string) error {
	value, err := s.redisCommand(ctx, "HINCRBY", key, "pending_jobs", "-1")
	if err != nil {
		return err
	}
	pending := int64(0)
	switch raw := value.(type) {
	case int64:
		pending = raw
	case string:
		pending, _ = strconv.ParseInt(raw, 10, 64)
	}
	if pending <= 0 {
		return s.finishAsyncRefresh(ctx, key)
	}
	return nil
}

func (s *Server) prepareContextReplyRefresh(c *echo.Context, status *models.Status, account *models.Account, requestID string) {
	if s == nil || c == nil || status == nil {
		return
	}
	key := "context:" + strconv.FormatInt(status.ID, 10) + ":refresh"
	statusValue, _ := s.redisCommand(c.Request().Context(), "HGET", key, "status")
	if current, ok := statusValue.(string); ok && current == "running" {
		if id, ok := asyncRefreshID(key, s.cfg.SecretKeyBase); ok {
			s.setAsyncRefreshHeader(c, id, 3)
		}
		return
	}
	if account == nil || !s.shouldFetchAllReplies(*status, time.Now().UTC()) {
		return
	}
	id, err := s.createAsyncRefresh(c.Request().Context(), key, true)
	if err != nil {
		return
	}
	if _, err := s.redisCommand(c.Request().Context(), "HSET", key, "pending_jobs", "1"); err != nil {
		_ = s.finishAsyncRefresh(c.Request().Context(), key)
		return
	}
	if !s.enqueueFetchAllRepliesTask(status.ID, requestID, key) {
		_ = s.finishAsyncRefresh(c.Request().Context(), key)
	}
	s.setAsyncRefreshHeader(c, id, 3)
}

func (s *Server) setAsyncRefreshHeader(c *echo.Context, id string, retrySeconds int) {
	if strings.TrimSpace(id) == "" {
		return
	}
	if retrySeconds <= 0 {
		retrySeconds = 3
	}
	value := `id="` + id + `", retry=` + strconv.Itoa(retrySeconds)
	if key, ok := asyncRefreshKeyFromID(id, s.cfg.SecretKeyBase); ok {
		if countValue, err := s.redisCommand(c.Request().Context(), "HGET", key, "result_count"); err == nil {
			switch raw := countValue.(type) {
			case string:
				if _, err := strconv.ParseInt(raw, 10, 64); err == nil {
					value += `, result_count=` + raw
				}
			case int64:
				value += `, result_count=` + strconv.FormatInt(raw, 10)
			}
		}
	}
	c.Response().Header().Set("Mastodon-Async-Refresh", value)
}
