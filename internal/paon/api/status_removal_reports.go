package api

import (
	"sort"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const removalReportAccountBatchSize = 500

type statusRemovalReportCandidate struct {
	StatusID  int64
	AccountID int64
}

// unresolvedReportedStatusIDs deliberately starts from indexed target_account_id
// lookups and performs bigint[] membership checks in Go. This avoids a correlated
// array-membership plan that can cause parallel sequential scans while many removal
// workers are active.
func unresolvedReportedStatusIDs(database *gorm.DB, candidates []statusRemovalReportCandidate) (map[int64]struct{}, error) {
	reported := make(map[int64]struct{})
	if database == nil || len(candidates) == 0 {
		return reported, nil
	}
	candidatesByAccount := make(map[int64]map[int64]struct{})
	for _, candidate := range candidates {
		if candidate.StatusID == 0 || candidate.AccountID == 0 {
			continue
		}
		if candidatesByAccount[candidate.AccountID] == nil {
			candidatesByAccount[candidate.AccountID] = make(map[int64]struct{})
		}
		candidatesByAccount[candidate.AccountID][candidate.StatusID] = struct{}{}
	}
	accountIDs := make([]int64, 0, len(candidatesByAccount))
	for accountID := range candidatesByAccount {
		accountIDs = append(accountIDs, accountID)
	}
	sort.Slice(accountIDs, func(i int, j int) bool { return accountIDs[i] < accountIDs[j] })
	for start := 0; start < len(accountIDs); start += removalReportAccountBatchSize {
		end := min(start+removalReportAccountBatchSize, len(accountIDs))
		var reports []models.Report
		if err := database.Model(&models.Report{}).
			Select("target_account_id", "status_ids").
			Where("target_account_id IN ? AND action_taken_at IS NULL", accountIDs[start:end]).
			Find(&reports).Error; err != nil {
			return nil, err
		}
		markReportedStatusIDs(reported, candidatesByAccount, reports)
	}
	return reported, nil
}

func markReportedStatusIDs(reported map[int64]struct{}, candidatesByAccount map[int64]map[int64]struct{}, reports []models.Report) {
	for _, report := range reports {
		candidates := candidatesByAccount[report.TargetAccountID]
		if len(candidates) == 0 {
			continue
		}
		for _, statusID := range report.StatusIDs {
			if _, ok := candidates[statusID]; ok {
				reported[statusID] = struct{}{}
			}
		}
	}
}

func statusRemovalReported(reported map[int64]struct{}, statusID int64) bool {
	_, ok := reported[statusID]
	return ok
}

func statusRemovalReportCandidatesFromStatuses(statuses []models.Status) []statusRemovalReportCandidate {
	candidates := make([]statusRemovalReportCandidate, 0, len(statuses))
	for _, status := range statuses {
		if status.ID != 0 && status.AccountID != 0 {
			candidates = append(candidates, statusRemovalReportCandidate{StatusID: status.ID, AccountID: status.AccountID})
		}
	}
	return candidates
}

func unreportedStatusIDs(statuses []models.Status, reported map[int64]struct{}) []int64 {
	ids := make([]int64, 0, len(statuses))
	for _, status := range statuses {
		if status.ID != 0 && !statusRemovalReported(reported, status.ID) {
			ids = append(ids, status.ID)
		}
	}
	return ids
}
