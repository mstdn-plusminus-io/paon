package serializer

import (
	"database/sql"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestNotificationFromModelLegacyType(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	notification := models.Notification{
		ID:           10,
		ActivityType: "Favourite",
		CreatedAt:    time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		FromAccount: models.Account{
			ID:        42,
			Username:  "alice",
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
	}

	out := NotificationFromModel(cfg, notification, nil)
	if out.ID != "10" {
		t.Fatalf("ID = %q", out.ID)
	}
	if out.Type != "favourite" {
		t.Fatalf("Type = %q", out.Type)
	}
	if out.Account.ID != "42" {
		t.Fatalf("Account.ID = %q", out.Account.ID)
	}
	if out.Status != nil {
		t.Fatalf("Status = %#v", out.Status)
	}
}

func TestNotificationFromModelIncludesAdminReport(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https"}
	notification := models.Notification{
		ID:           10,
		ActivityID:   20,
		ActivityType: "Report",
		Type:         "admin.report",
		CreatedAt:    time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		FromAccount: models.Account{
			ID:        42,
			Username:  "alice",
			CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
		},
		Report: &models.Report{
			ID:       20,
			Category: 2000,
			Forwarded: sql.NullBool{
				Bool:  true,
				Valid: true,
			},
			CreatedAt: time.Date(2026, 6, 18, 11, 0, 0, 0, time.UTC),
			TargetAccount: models.Account{
				ID:        84,
				Username:  "target",
				CreatedAt: time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC),
			},
		},
	}

	out := NotificationFromModel(cfg, notification, nil)
	if out.Type != "admin.report" {
		t.Fatalf("Type = %q", out.Type)
	}
	report, ok := out.Report.(Report)
	if !ok {
		t.Fatalf("Report type = %T", out.Report)
	}
	if report.ID != "20" || report.Category != "violation" || report.TargetAccount.ID != "84" {
		t.Fatalf("Report = %#v", report)
	}
}
