package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// listImportDryRunConnector opens connections that can begin, commit, and roll
// back transactions but never run a statement. The dry-run database built on it
// only renders SQL, so processListImportRow can run its row transaction.
type listImportDryRunConnector struct{}

func (listImportDryRunConnector) Connect(context.Context) (driver.Conn, error) {
	return listImportDryRunConn{}, nil
}

func (listImportDryRunConnector) Driver() driver.Driver { return listImportDryRunDriver{} }

type listImportDryRunDriver struct{}

func (listImportDryRunDriver) Open(string) (driver.Conn, error) { return listImportDryRunConn{}, nil }

type listImportDryRunConn struct{}

func (listImportDryRunConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("list import dry run: statements are never executed")
}

func (listImportDryRunConn) Close() error { return nil }

func (listImportDryRunConn) Begin() (driver.Tx, error) { return listImportDryRunTx{}, nil }

type listImportDryRunTx struct{}

func (listImportDryRunTx) Commit() error   { return nil }
func (listImportDryRunTx) Rollback() error { return nil }

const (
	listImportDryRunListID          = int64(3)
	listImportDryRunFollowID        = int64(11)
	listImportDryRunFollowRequestID = int64(12)
)

// listImportDryRunDB is a dry-run database holding the importer and the target
// accounts, the importer's list, and a bulk import that is not finished yet.
// There is no follow or follow request until the import inserts one; lookups
// of anything else miss.
func listImportDryRunDB(t *testing.T, capture *statusMentionSQLCapture, importer models.Account, target models.Account) *gorm.DB {
	t.Helper()
	sqlDB := sql.OpenDB(listImportDryRunConnector{})
	t.Cleanup(func() { _ = sqlDB.Close() })
	database, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DryRun:                 true,
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
		Logger:                 capture,
	})
	if err != nil {
		t.Fatal(err)
	}
	followCreated, requestCreated := false, false
	err = database.Callback().Create().After("gorm:create").Register("paon_test:list_import_create", func(db *gorm.DB) {
		switch db.Statement.Dest.(type) {
		case *models.Follow:
			followCreated = true
			db.RowsAffected = 1
		case *models.FollowRequest:
			requestCreated = true
			db.RowsAffected = 1
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	err = database.Callback().Query().After("gorm:query").Register("paon_test:list_import_query", func(db *gorm.DB) {
		// Only First/Take/Last report a missing row; Find and Count stay empty.
		if db.Error != nil || !db.Statement.RaiseErrorOnNotFound {
			return
		}
		switch dest := db.Statement.Dest.(type) {
		case *models.Account:
			for _, account := range []models.Account{importer, target} {
				for _, value := range db.Statement.Vars {
					if value == account.ID || value == strings.ToLower(account.Username) {
						*dest = account
						db.RowsAffected = 1
						return
					}
				}
			}
		case *models.List:
			*dest = models.List{ID: listImportDryRunListID, AccountID: importer.ID, Title: "friends"}
			db.RowsAffected = 1
			return
		case *models.Follow:
			if followCreated {
				*dest = models.Follow{ID: listImportDryRunFollowID, AccountID: importer.ID, TargetAccountID: target.ID}
				db.RowsAffected = 1
				return
			}
		case *models.FollowRequest:
			if requestCreated {
				*dest = models.FollowRequest{ID: listImportDryRunFollowRequestID, AccountID: importer.ID, TargetAccountID: target.ID}
				db.RowsAffected = 1
				return
			}
		case *models.BulkImport:
			*dest = models.BulkImport{ID: 1, ProcessedItems: 1, TotalItems: 2}
			db.RowsAffected = 1
			return
		}
		_ = db.AddError(gorm.ErrRecordNotFound)
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

// BulkImportRowService#call for lists runs FollowService for an account the
// importer neither follows nor requested, then adds it to the list, where
// ListAccount#set_follow links the new follow or follow request.
func TestListImportFollowsUnfollowedAccountsBeforeListingThem(t *testing.T) {
	importer := models.Account{ID: 7, Username: "bob"}
	for _, tt := range []struct {
		name       string
		target     models.Account
		wantInsert string
		noInsert   string
		wantListed string
	}{
		{
			name:       "unlocked local account is followed",
			target:     models.Account{ID: 9, Username: "carol"},
			wantInsert: `INSERT INTO "follows"`,
			noInsert:   `INSERT INTO "follow_requests"`,
			wantListed: "(3,9,11,NULL)",
		},
		{
			name:       "locked local account is requested",
			target:     models.Account{ID: 9, Username: "dave", Locked: true},
			wantInsert: `INSERT INTO "follow_requests"`,
			noInsert:   `INSERT INTO "follows"`,
			wantListed: "(3,9,NULL,12)",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			capture := &statusMentionSQLCapture{}
			s := &Server{
				cfg: config.Config{Scheme: "https", LocalDomain: "paon.example", WebDomain: "paon.example", RedisHost: "127.0.0.1", RedisPort: "1"},
				db:  listImportDryRunDB(t, capture, importer, tt.target),
			}
			bulkImport := models.BulkImport{ID: 1, AccountID: importer.ID}
			row := models.BulkImportRow{ID: 5, BulkImportID: bulkImport.ID, Data: models.JSONValue(`{"list_name":"friends","acct":"` + tt.target.Username + `"}`)}

			if err := s.processListImportRow(context.Background(), bulkImport, row); err != nil {
				t.Fatal(err)
			}

			queries := strings.Join(capture.queries, "\n")
			relationshipInsert := strings.Index(queries, tt.wantInsert)
			listInsert := strings.Index(queries, `INSERT INTO "list_accounts"`)
			if relationshipInsert < 0 || listInsert < relationshipInsert {
				t.Fatalf("list import must run %s before adding the account to the list:\n%s", tt.wantInsert, queries)
			}
			if strings.Contains(queries, tt.noInsert) {
				t.Fatalf("list import must not run %s:\n%s", tt.noInsert, queries)
			}
			if listed := queries[listInsert:]; !strings.Contains(listed, tt.wantListed) {
				t.Fatalf("list account insert = %q, want values %s", listed, tt.wantListed)
			}
			for _, want := range []string{`DELETE FROM "bulk_import_rows" WHERE id = 5`, `imported_items + 1`} {
				if !strings.Contains(queries, want) {
					t.Fatalf("imported list row must be counted and removed; missing %q:\n%s", want, queries)
				}
			}
		})
	}
}
