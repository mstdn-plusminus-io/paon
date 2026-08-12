package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const defaultMeiliDeployBatchSize = 100
const defaultMeiliDeployProgressPath = "tmp/meilisearch_deploy_progress.json"

const (
	meiliDeployModelAccount  = "Account"
	meiliDeployModelStatus   = "Status"
	meiliDeployModelTag      = "Tag"
	meiliDeployModelInstance = "Instance"
)

type MeiliDeployOptions struct {
	BatchSize    int
	Resume       bool
	OnlyMapping  bool
	ProgressPath string
	Writer       io.Writer
}

type MeiliDeployStats struct {
	Accounts  int
	Statuses  int
	Tags      int
	Instances int
}

type meiliDeployProgress struct {
	Model               string `json:"model"`
	LastProcessedID     int64  `json:"last_processed_id,omitempty"`
	LastProcessedDomain string `json:"last_processed_domain,omitempty"`
}

func (s *Server) DeployMeiliIndexes(ctx context.Context, options MeiliDeployOptions) (MeiliDeployStats, error) {
	if !s.cfg.MeiliEnabled || strings.TrimSpace(s.cfg.MeiliHost) == "" {
		return MeiliDeployStats{}, errMeiliDisabled
	}
	if s.db == nil {
		return MeiliDeployStats{}, errors.New("DATABASE_URL is not set")
	}
	if options.BatchSize <= 0 {
		options.BatchSize = defaultMeiliDeployBatchSize
	}
	if options.ProgressPath == "" {
		options.ProgressPath = defaultMeiliDeployProgressPath
	}
	progress := meiliDeployProgress{}
	if options.Resume {
		loaded, ok, err := readMeiliDeployProgress(options.ProgressPath)
		if err != nil {
			return MeiliDeployStats{}, err
		}
		if ok {
			progress = loaded
		}
		if progress.Model != "" && !validMeiliDeployModel(progress.Model) {
			return MeiliDeployStats{}, fmt.Errorf("invalid Meilisearch deploy progress model %q", progress.Model)
		}
	}
	if err := s.syncMeiliIndexes(ctx); err != nil {
		return MeiliDeployStats{}, err
	}
	if options.OnlyMapping {
		return MeiliDeployStats{}, nil
	}
	saveProgress := func(progress meiliDeployProgress) error {
		if err := writeMeiliDeployProgress(options.ProgressPath, progress); err != nil {
			return err
		}
		meiliDeployProgressLog(options.Writer, options.ProgressPath, progress)
		return nil
	}

	var stats MeiliDeployStats
	if shouldRunMeiliDeployModel(progress.Model, meiliDeployModelAccount) {
		count, err := s.deployMeiliAccounts(ctx, options.BatchSize, meiliDeployStartID(progress, meiliDeployModelAccount), saveProgress)
		if err != nil {
			return stats, err
		}
		stats.Accounts = count
		meiliDeployLog(options.Writer, "accounts", count)
	}

	if shouldRunMeiliDeployModel(progress.Model, meiliDeployModelStatus) {
		count, err := s.deployMeiliStatuses(ctx, options.BatchSize, meiliDeployStartID(progress, meiliDeployModelStatus), saveProgress)
		if err != nil {
			return stats, err
		}
		stats.Statuses = count
		meiliDeployLog(options.Writer, "statuses", count)
	}

	if shouldRunMeiliDeployModel(progress.Model, meiliDeployModelTag) {
		count, err := s.deployMeiliTags(ctx, options.BatchSize, meiliDeployStartID(progress, meiliDeployModelTag), saveProgress)
		if err != nil {
			return stats, err
		}
		stats.Tags = count
		meiliDeployLog(options.Writer, "tags", count)
	}

	if shouldRunMeiliDeployModel(progress.Model, meiliDeployModelInstance) {
		count, err := s.deployMeiliInstances(ctx, options.BatchSize, meiliDeployStartDomain(progress), saveProgress)
		if err != nil {
			return stats, err
		}
		stats.Instances = count
		meiliDeployLog(options.Writer, "instances", count)
	}

	_ = os.Remove(options.ProgressPath)
	return stats, nil
}

func (s *Server) deployMeiliAccounts(ctx context.Context, batchSize int, startID int64, saveProgress func(meiliDeployProgress) error) (int, error) {
	total := 0
	lastID := startID
	for {
		var accounts []models.Account
		err := s.db.WithContext(ctx).
			Preload("AccountStat").
			Where("id > ? AND suspended_at IS NULL AND moved_to_account_id IS NULL AND discoverable = ?", lastID, true).
			Order("id ASC").
			Limit(batchSize).
			Find(&accounts).Error
		if err != nil {
			return total, err
		}
		if len(accounts) == 0 {
			return total, nil
		}
		documents := make([]meiliAccountDocument, 0, len(accounts))
		for _, account := range accounts {
			lastID = account.ID
			if meiliAccountSearchable(account) {
				documents = append(documents, s.meiliAccountDocument(account))
			}
		}
		if len(documents) > 0 {
			if err := s.meiliUpsertDocuments(ctx, "accounts", documents); err != nil {
				return total, err
			}
			total += len(documents)
		}
		if err := saveProgress(meiliDeployProgress{Model: meiliDeployModelAccount, LastProcessedID: lastID}); err != nil {
			return total, err
		}
	}
}

func (s *Server) deployMeiliStatuses(ctx context.Context, batchSize int, startID int64, saveProgress func(meiliDeployProgress) error) (int, error) {
	total := 0
	lastID := startID
	for {
		var statuses []models.Status
		err := s.meiliStatusDeployQuery(ctx).
			Where("statuses.id > ? AND statuses.deleted_at IS NULL AND statuses.visibility IN ?", lastID, []int{0, 1, 2, 3}).
			Order("statuses.id ASC").
			Limit(batchSize).
			Find(&statuses).Error
		if err != nil {
			return total, err
		}
		if len(statuses) == 0 {
			return total, nil
		}
		documents := make([]meiliStatusDocument, 0, len(statuses))
		for _, status := range statuses {
			lastID = status.ID
			if meiliStatusSearchable(status) {
				documents = append(documents, s.meiliStatusDocument(status))
			}
		}
		if len(documents) > 0 {
			if err := s.meiliUpsertDocuments(ctx, "statuses", documents); err != nil {
				return total, err
			}
			total += len(documents)
		}
		if err := saveProgress(meiliDeployProgress{Model: meiliDeployModelStatus, LastProcessedID: lastID}); err != nil {
			return total, err
		}
	}
}

func (s *Server) deployMeiliTags(ctx context.Context, batchSize int, startID int64, saveProgress func(meiliDeployProgress) error) (int, error) {
	total := 0
	lastID := startID
	for {
		var tags []models.Tag
		err := s.db.WithContext(ctx).
			Where("id > ? AND listable = ?", lastID, true).
			Order("id ASC").
			Limit(batchSize).
			Find(&tags).Error
		if err != nil {
			return total, err
		}
		if len(tags) == 0 {
			return total, nil
		}
		documents := make([]meiliTagDocument, 0, len(tags))
		for _, tag := range tags {
			lastID = tag.ID
			if meiliTagListable(tag) {
				documents = append(documents, s.meiliTagDocument(tag))
			}
		}
		if len(documents) > 0 {
			if err := s.meiliUpsertDocuments(ctx, "tags", documents); err != nil {
				return total, err
			}
			total += len(documents)
		}
		if err := saveProgress(meiliDeployProgress{Model: meiliDeployModelTag, LastProcessedID: lastID}); err != nil {
			return total, err
		}
	}
}

func (s *Server) deployMeiliInstances(ctx context.Context, batchSize int, startDomain string, saveProgress func(meiliDeployProgress) error) (int, error) {
	total := 0
	lastDomain := startDomain
	for {
		var instances []models.Instance
		query := s.db.WithContext(ctx).Where("domain <> ''")
		if lastDomain != "" {
			query = query.Where("domain > ?", lastDomain)
		}
		err := query.Order("domain ASC").Limit(batchSize).Find(&instances).Error
		if err != nil {
			return total, err
		}
		if len(instances) == 0 {
			return total, nil
		}
		documents := make([]meiliInstanceDocument, 0, len(instances))
		for _, instance := range instances {
			lastDomain = instance.Domain
			if meiliInstanceSearchable(instance) {
				documents = append(documents, meiliInstanceDocument{
					ID:            instance.Domain,
					Domain:        instance.Domain,
					AccountsCount: instance.AccountsCount,
				})
			}
		}
		if len(documents) > 0 {
			if err := s.meiliUpsertDocuments(ctx, "instances", documents); err != nil {
				return total, err
			}
			total += len(documents)
		}
		if err := saveProgress(meiliDeployProgress{Model: meiliDeployModelInstance, LastProcessedDomain: lastDomain}); err != nil {
			return total, err
		}
	}
}

func (s *Server) meiliStatusDeployQuery(ctx context.Context) *gorm.DB {
	query := s.db.WithContext(ctx).
		Preload("StatusStat").
		Preload("MediaAttachments").
		Preload("Mentions").
		Preload("Tags").
		Preload("PreviewCards").
		Preload("Poll").
		Preload("Quote")
	if s.cfg.MeiliLibraryOnly {
		query = query.Where(meiliLibraryOnlyStatusSQL())
	}
	return query
}

func meiliDeployLog(writer io.Writer, name string, count int) {
	if writer == nil {
		return
	}
	_, _ = fmt.Fprintln(writer, name+": "+strconv.Itoa(count))
}

func meiliDeployProgressLog(writer io.Writer, path string, progress meiliDeployProgress) {
	if writer == nil {
		return
	}
	_, _ = fmt.Fprintln(writer, "progress saved")
	_, _ = fmt.Fprintln(writer, "  model: "+progress.Model)
	if progress.LastProcessedID > 0 {
		_, _ = fmt.Fprintln(writer, "  last processed ID: "+strconv.FormatInt(progress.LastProcessedID, 10))
	}
	if progress.LastProcessedDomain != "" {
		_, _ = fmt.Fprintln(writer, "  last processed domain: "+progress.LastProcessedDomain)
	}
	_, _ = fmt.Fprintln(writer, "  progress file: "+path)
	_, _ = fmt.Fprintln(writer, "  resume: RESUME=true paon-meili-deploy")
}

func readMeiliDeployProgress(path string) (meiliDeployProgress, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return meiliDeployProgress{}, false, nil
		}
		return meiliDeployProgress{}, false, err
	}
	var raw struct {
		Model               string `json:"model"`
		CurrentModel        string `json:"current_model"`
		CurrentModelIndex   *int   `json:"current_model_index"`
		LastProcessedID     int64  `json:"last_processed_id"`
		LastProcessedDomain string `json:"last_processed_domain"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return meiliDeployProgress{}, false, err
	}
	progress := meiliDeployProgress{
		Model:               raw.Model,
		LastProcessedID:     raw.LastProcessedID,
		LastProcessedDomain: raw.LastProcessedDomain,
	}
	if progress.Model == "" {
		progress.Model = raw.CurrentModel
	}
	if progress.Model == "" && raw.CurrentModelIndex != nil {
		progress.Model = meiliDeployModelFromRailsIndex(*raw.CurrentModelIndex)
	}
	return progress, true, nil
}

func meiliDeployModelFromRailsIndex(index int) string {
	models := []string{meiliDeployModelAccount, meiliDeployModelStatus, meiliDeployModelTag, meiliDeployModelInstance}
	if index < 0 || index >= len(models) {
		return ""
	}
	return models[index]
}

func writeMeiliDeployProgress(path string, progress meiliDeployProgress) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func validMeiliDeployModel(model string) bool {
	if model == "" {
		return true
	}
	_, ok := meiliDeployModelRank(model)
	return ok
}

func shouldRunMeiliDeployModel(progressModel string, model string) bool {
	if progressModel == "" {
		return true
	}
	progressRank, ok := meiliDeployModelRank(progressModel)
	if !ok {
		return false
	}
	modelRank, ok := meiliDeployModelRank(model)
	return ok && modelRank >= progressRank
}

func meiliDeployModelRank(model string) (int, bool) {
	switch model {
	case meiliDeployModelAccount:
		return 0, true
	case meiliDeployModelStatus:
		return 1, true
	case meiliDeployModelTag:
		return 2, true
	case meiliDeployModelInstance:
		return 3, true
	default:
		return 0, false
	}
}

func meiliDeployStartID(progress meiliDeployProgress, model string) int64 {
	if progress.Model == model {
		return progress.LastProcessedID
	}
	return 0
}

func meiliDeployStartDomain(progress meiliDeployProgress) string {
	if progress.Model == meiliDeployModelInstance {
		return progress.LastProcessedDomain
	}
	return ""
}
