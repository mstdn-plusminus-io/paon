package api

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func (s *Server) resolveSearchURL(query string, searchType string, offset int, current ...*models.Account) ([]models.Account, []models.Status, bool, error) {
	if !searchURLQuery(query) {
		return nil, nil, false, nil
	}
	if offset > 0 {
		return nil, nil, true, nil
	}
	if searchIncludesType(searchType, "accounts") {
		account, err := s.resolveSearchAccountURL(query)
		if err != nil {
			return nil, nil, true, err
		}
		if account != nil && account.ID != 0 && !account.SuspendedAt.Valid {
			return []models.Account{*account}, nil, true, nil
		}
	}
	if searchIncludesType(searchType, "statuses") {
		status, err := s.resolveSearchStatusURL(query, firstAccount(current))
		if err != nil {
			return nil, nil, true, err
		}
		if status != nil && status.ID != 0 {
			return nil, []models.Status{*status}, true, nil
		}
	}
	return nil, nil, true, nil
}

func searchURLQuery(query string) bool {
	query = strings.TrimSpace(query)
	return strings.HasPrefix(query, "http://") || strings.HasPrefix(query, "https://")
}

func (s *Server) resolveSearchAccountURL(raw string) (*models.Account, error) {
	if s.db == nil {
		return nil, nil
	}
	if acct := s.localRemoteAccountAcctFromURL(raw); acct != "" {
		account, err := s.findAccountByAcct(acct)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return account, err
	}
	if username := s.localAccountUsernameFromURL(raw); username != "" {
		var account models.Account
		err := s.db.Preload("AccountStat").Preload("User.Role").
			Where("lower(username) = ? AND (domain IS NULL OR domain = '')", strings.ToLower(username)).
			First(&account).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return &account, err
	}
	if !s.localActivityURI(raw) {
		return s.resolveSearchRemoteAccountURL(raw)
	}
	return nil, nil
}

func (s *Server) resolveSearchRemoteAccountURL(raw string) (*models.Account, error) {
	actor, err := s.fetchActivityActor(raw)
	if err == nil && actor.ID != "" && actor.PublicKey.PublicKeyPem != "" {
		if err := verifyRemoteActivityActorWebFinger(actor); err != nil {
			return nil, nil
		}
		return s.upsertRemoteActivityActorForRequest(actor, remoteStatusDiscoveryRequestID("", raw))
	}
	if !resolveSearchAccountKnownFallbackAllowed(err) {
		return nil, nil
	}
	return s.resolveKnownRemoteAccountFromDB(raw)
}

func resolveSearchAccountKnownFallbackAllowed(err error) bool {
	if err == nil {
		return false
	}
	if status, ok := activityFetchStatus(err); ok {
		return status == http.StatusInternalServerError ||
			status == http.StatusBadGateway ||
			status == http.StatusServiceUnavailable ||
			status == http.StatusGatewayTimeout
	}
	return true
}

func (s *Server) resolveKnownRemoteAccountFromDB(raw string) (*models.Account, error) {
	var account models.Account
	err := s.db.Preload("AccountStat").Preload("User.Role").
		Where("uri = ?", raw).
		First(&account).Error
	if err == nil {
		return &account, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return nil, nil
}

func (s *Server) resolveSearchStatusURL(raw string, current ...*models.Account) (*models.Status, error) {
	if s.db == nil {
		return nil, nil
	}
	account := firstAccount(current)
	if id := statusIDFromLocalURL(s.cfg.BaseURL(), raw); id != "" {
		status, err := s.findVisibleStatusForAccount(account, id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return status, err
	}
	if id := localActivityStatusID(s, raw); id != "" {
		status, err := s.findVisibleStatusForAccount(account, id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return status, err
	}
	if s.localActivityURI(raw) {
		return nil, nil
	}
	return s.fetchVisibleRemoteStatusFromResolvableURL(raw, account)
}

func localActivityStatusID(s *Server, raw string) string {
	if s == nil {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || !s.localActivityHost(parsed.Host) {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[0] != "users" || parts[2] != "statuses" {
		return ""
	}
	if _, err := strconv.ParseInt(parts[3], 10, 64); err != nil {
		return ""
	}
	return parts[3]
}

func firstAccount(accounts []*models.Account) *models.Account {
	if len(accounts) == 0 {
		return nil
	}
	return accounts[0]
}

func (s *Server) resolveKnownRemoteStatusFromDB(raw string, current *models.Account) (*models.Status, error) {
	if s.db == nil || current == nil || current.ID == 0 {
		return nil, nil
	}
	query := s.visibleStatusQuery(current)
	if canonical := railsRemoteStatusURIFromWebURL(raw); canonical != "" && canonical != raw {
		query = query.Where("(statuses.uri = ? OR (statuses.uri = ? AND statuses.url = ?))", raw, canonical, raw)
	} else if knownPrivateStatusURL(raw) {
		query = query.Where("(statuses.uri = ? OR statuses.url = ?)", raw, raw)
	} else {
		query = query.Where("statuses.uri = ?", raw)
	}
	var status models.Status
	err := query.First(&status).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.hydrateStatusCustomEmojis(&status); err != nil {
		return nil, err
	}
	return &status, nil
}

var knownPrivateStatusPathPattern = regexp.MustCompile(`^/@[^/]+/(?:statuses/)?[0-9A-Za-z]+$`)

func knownPrivateStatusURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return knownPrivateStatusPathPattern.MatchString(parsed.EscapedPath())
}

func railsRemoteStatusURIFromWebURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "@") || parts[1] == "" {
		return ""
	}
	if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
		return ""
	}
	parsed.Path = "/users/" + strings.TrimPrefix(parts[0], "@") + "/statuses/" + parts[1]
	parsed.RawPath = ""
	return parsed.String()
}

func (s *Server) localAccountUsernameFromURL(raw string) string {
	base, err := url.Parse(s.cfg.BaseURL())
	if err != nil || base.Host == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Host, base.Host) {
		return ""
	}
	parts := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(parts) == 1 && strings.HasPrefix(parts[0], "@") {
		return strings.TrimPrefix(parts[0], "@")
	}
	if len(parts) == 2 && strings.HasPrefix(parts[0], "@") {
		if _, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			return ""
		}
		return strings.TrimPrefix(parts[0], "@")
	}
	if len(parts) == 2 && parts[0] == "users" && parts[1] != "" {
		return parts[1]
	}
	return ""
}

func (s *Server) localRemoteAccountAcctFromURL(raw string) string {
	base, err := url.Parse(s.cfg.BaseURL())
	if err != nil || base.Host == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Host, base.Host) {
		return ""
	}
	parts := strings.Split(strings.Trim(path.Clean(parsed.Path), "/"), "/")
	if len(parts) != 1 || !strings.HasPrefix(parts[0], "@") {
		return ""
	}
	acct := strings.TrimPrefix(parts[0], "@")
	if username, domain, ok := strings.Cut(acct, "@"); ok && username != "" && domain != "" {
		return acct
	}
	return ""
}
