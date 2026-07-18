package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const softwareUpdateCheckWorkerInterval = 30 * time.Minute

type softwareUpdateCheckResponse struct {
	UpdatesAvailable []softwareUpdateNotice `json:"updatesAvailable"`
}

type softwareUpdateNotice struct {
	Version      string `json:"version"`
	Urgent       bool   `json:"urgent"`
	Type         any    `json:"type"`
	ReleaseNotes string `json:"releaseNotes"`
}

func (s *Server) runSoftwareUpdateCheckWorker(ctx context.Context) {
	ticker := time.NewTicker(softwareUpdateCheckWorkerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runSchedulerWithRedisLock(ctx, "software_update_check_scheduler", time.Hour, func() {
				s.checkSoftwareUpdates(ctx)
			})
		}
	}
}

func (s *Server) checkSoftwareUpdates(ctx context.Context) int {
	if s == nil || s.db == nil {
		return 0
	}
	s.cleanOutdatedSoftwareUpdates(ctx)
	if !s.softwareUpdateCheckEnabled() {
		return 0
	}
	response, ok := s.fetchSoftwareUpdateNotices(ctx)
	if !ok {
		return 0
	}
	return s.processSoftwareUpdateNotices(ctx, response.UpdatesAvailable)
}

func (s *Server) cleanOutdatedSoftwareUpdates(ctx context.Context) int {
	if s == nil || s.db == nil {
		return 0
	}
	var updates []models.SoftwareUpdate
	if err := s.db.WithContext(ctx).Find(&updates).Error; err != nil {
		return 0
	}
	deleted := 0
	current := s.currentSoftwareUpdateVersion()
	for _, update := range updates {
		if strings.TrimSpace(update.Version) == "" || compareSoftwareVersions(current, update.Version) >= 0 {
			if err := s.db.WithContext(ctx).Delete(&models.SoftwareUpdate{}, update.ID).Error; err == nil {
				deleted++
			}
		}
	}
	return deleted
}

func (s *Server) fetchSoftwareUpdateNotices(ctx context.Context) (softwareUpdateCheckResponse, bool) {
	endpoint := s.cfg.UpdateCheckURL
	if endpoint == "" {
		return softwareUpdateCheckResponse{}, false
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return softwareUpdateCheckResponse{}, false
	}
	query := u.Query()
	query.Set("version", s.currentSoftwareUpdateVersion())
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return softwareUpdateCheckResponse{}, false
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mastodon update checker")
	client := activityHTTPClient
	if client == nil {
		return softwareUpdateCheckResponse{}, false
	}
	res, err := client.Do(req)
	if err != nil {
		return softwareUpdateCheckResponse{}, false
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return softwareUpdateCheckResponse{}, false
	}
	body, err := readActivityResponseBodyWithRailsLimit(res, "software-update")
	if err != nil {
		return softwareUpdateCheckResponse{}, false
	}
	var response softwareUpdateCheckResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return softwareUpdateCheckResponse{}, false
	}
	return response, true
}

func (s *Server) processSoftwareUpdateNotices(ctx context.Context, notices []softwareUpdateNotice) int {
	if s == nil || s.db == nil {
		return 0
	}
	versions := make([]string, 0, len(notices))
	for _, notice := range notices {
		versions = append(versions, notice.Version)
	}
	if len(versions) == 0 {
		_ = s.db.WithContext(ctx).Where("1 = 1").Delete(&models.SoftwareUpdate{}).Error
		return 0
	}
	if err := s.db.WithContext(ctx).Where("version NOT IN ?", versions).Delete(&models.SoftwareUpdate{}).Error; err != nil {
		return 0
	}
	var known []string
	if err := s.db.WithContext(ctx).Model(&models.SoftwareUpdate{}).Where("version IN ?", versions).Pluck("version", &known).Error; err != nil {
		return 0
	}
	seen := make(map[string]struct{}, len(known))
	for _, version := range known {
		seen[version] = struct{}{}
	}
	now := time.Now().UTC()
	newUpdates := make([]models.SoftwareUpdate, 0)
	for _, notice := range notices {
		version := notice.Version
		if _, ok := seen[version]; ok {
			continue
		}
		update := models.SoftwareUpdate{
			Version:      version,
			Urgent:       notice.Urgent,
			Type:         softwareUpdateTypeValue(notice.Type),
			ReleaseNotes: notice.ReleaseNotes,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.db.WithContext(ctx).Create(&update).Error; err != nil {
			continue
		}
		newUpdates = append(newUpdates, update)
		seen[version] = struct{}{}
	}
	if len(newUpdates) > 0 {
		_ = s.sendSoftwareUpdateMails(newUpdates)
	}
	return len(newUpdates)
}

func (s *Server) currentSoftwareUpdateVersion() string {
	version := strings.TrimSpace(s.cfg.Version)
	if !softwareVersionLooksNumeric(version) {
		version = strings.TrimSpace(s.cfg.MastodonVersion)
	}
	if idx := strings.Index(version, "+"); idx >= 0 {
		version = version[:idx]
	}
	return strings.TrimSpace(version)
}

func softwareVersionLooksNumeric(version string) bool {
	version = strings.TrimSpace(version)
	return version != "" && version[0] >= '0' && version[0] <= '9'
}

func softwareUpdateTypeValue(value any) int {
	switch v := value.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "minor":
			return 1
		case "major":
			return 2
		default:
			return 0
		}
	case float64:
		if v == 1 || v == 2 {
			return int(v)
		}
	case int:
		if v == 1 || v == 2 {
			return v
		}
	}
	return 0
}
