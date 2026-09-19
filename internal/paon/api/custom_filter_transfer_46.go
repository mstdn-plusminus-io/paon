package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type customFilterTransferKeyword struct {
	Keyword   string `json:"keyword"`
	WholeWord bool   `json:"whole_word"`
}

type customFilterTransferRow struct {
	Title              string                        `json:"title"`
	ExpiresAt          any                           `json:"expires_at"`
	Context            models.StringArray            `json:"context"`
	Action             string                        `json:"action"`
	KeywordsAttributes []customFilterTransferKeyword `json:"keywords_attributes"`
	Statuses           []string                      `json:"statuses"`
}

func (s *Server) exportCustomFiltersJSON(c *echo.Context) error {
	account, _, _, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	var filters []models.CustomFilter
	if err := s.db.Preload("Keywords").Preload("Statuses.Status.Account").Where("account_id = ?", account.ID).Order("phrase ASC").Find(&filters).Error; err != nil {
		return err
	}
	rows := make([]customFilterTransferRow, 0, len(filters))
	for _, filter := range filters {
		keywords := make([]customFilterTransferKeyword, 0, len(filter.Keywords))
		for _, keyword := range filter.Keywords {
			keywords = append(keywords, customFilterTransferKeyword{Keyword: keyword.Keyword, WholeWord: keyword.WholeWord})
		}
		statuses := []string{}
		for _, filtered := range filter.Statuses {
			if filtered.Status != nil {
				statuses = append(statuses, activityPubStatusURI(s, *filtered.Status))
			}
		}
		var expires any
		if filter.ExpiresAt.Valid {
			expires = filter.ExpiresAt.Time.UTC()
		}
		rows = append(rows, customFilterTransferRow{
			Title: filter.Phrase, ExpiresAt: expires, Context: filter.Context, Action: customFilterTransferAction(filter.Action),
			KeywordsAttributes: keywords, Statuses: statuses,
		})
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="custom_filters.json"`)
	return c.JSON(http.StatusOK, map[string]any{"custom_filters": rows})
}

func customFilterTransferAction(action int) string {
	switch action {
	case 1:
		return "hide"
	case 2:
		return "blur"
	default:
		return "warn"
	}
}

func parseCustomFilterImport(data []byte) ([]map[string]any, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	raw, ok := envelope["custom_filters"]
	if !ok || string(raw) == "null" {
		return nil, fmt.Errorf("JSON file is incompatible with the selected import type")
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if len(rows) > importRowsProcessingLimit {
		return nil, fmt.Errorf("JSON file has more than %d rows", importRowsProcessingLimit)
	}
	return rows, nil
}

func (s *Server) customFilterImportMissingStatus(rows []map[string]any) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	seen := map[string]struct{}{}
	for _, row := range rows {
		for _, value := range anySlice(row["statuses"]) {
			uri := strings.TrimSpace(fmt.Sprint(value))
			if uri != "" {
				seen[uri] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return false, nil
	}
	uris := make([]string, 0, len(seen))
	for uri := range seen {
		uris = append(uris, uri)
	}
	var count int64
	if err := s.db.Model(&models.Status{}).Where("uri IN ?", uris).Count(&count).Error; err != nil {
		return false, err
	}
	return count != int64(len(uris)), nil
}

func sortedTransferStatusURIs(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
