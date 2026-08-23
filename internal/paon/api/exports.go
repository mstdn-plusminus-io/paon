package api

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type exportAccountRow struct {
	Username string         `gorm:"column:username"`
	Domain   sql.NullString `gorm:"column:domain"`
}

type exportFollowRow struct {
	Username    string             `gorm:"column:username"`
	Domain      sql.NullString     `gorm:"column:domain"`
	ShowReblogs bool               `gorm:"column:show_reblogs"`
	Notify      bool               `gorm:"column:notify"`
	Languages   models.StringArray `gorm:"column:languages"`
}

type exportMuteRow struct {
	Username          string         `gorm:"column:username"`
	Domain            sql.NullString `gorm:"column:domain"`
	HideNotifications bool           `gorm:"column:hide_notifications"`
}

type exportBookmarkRow struct {
	StatusID        int64          `gorm:"column:status_id"`
	StatusURI       sql.NullString `gorm:"column:status_uri"`
	StatusURL       sql.NullString `gorm:"column:status_url"`
	AccountID       int64          `gorm:"column:account_id"`
	AccountUsername string         `gorm:"column:account_username"`
	AccountDomain   sql.NullString `gorm:"column:account_domain"`
	AccountIDScheme sql.NullInt64  `gorm:"column:account_id_scheme"`
}

type exportListRow struct {
	Title    string         `gorm:"column:title"`
	Username string         `gorm:"column:username"`
	Domain   sql.NullString `gorm:"column:domain"`
}

type exportDomainBlockRow struct {
	Domain models.NullSafeString `gorm:"column:domain"`
}

func (s *Server) exportFollowsCSV(c *echo.Context) error {
	account, _, _, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	body, err := s.exportFollowsCSVBytes(account.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="follows.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", body)
}

func (s *Server) exportFollowsCSVBytes(accountID int64) ([]byte, error) {
	var rows []exportFollowRow
	if err := s.db.Raw(`
		SELECT accounts.username, accounts.domain, follows.show_reblogs, follows.notify, follows.languages
		FROM follows
		INNER JOIN accounts ON accounts.id = follows.target_account_id
		WHERE follows.account_id = ?
		ORDER BY follows.id DESC
	`, accountID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return csvBytes("follows.csv", [][]string{
		{"Account address", "Show boosts", "Notify on new posts", "Languages"},
	}, func(w *csv.Writer) error {
		for _, row := range rows {
			if err := w.Write([]string{
				s.exportAccountAddress(exportAccountRow{Username: row.Username, Domain: row.Domain}),
				strconv.FormatBool(row.ShowReblogs),
				strconv.FormatBool(row.Notify),
				strings.Join(row.Languages, ", "),
			}); err != nil {
				return err
			}
		}
		return nil
	}), nil
}

func (s *Server) exportBlocksCSV(c *echo.Context) error {
	account, _, _, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	body, err := s.exportBlocksCSVBytes(account.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="blocks.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", body)
}

func (s *Server) exportBlocksCSVBytes(accountID int64) ([]byte, error) {
	var rows []exportAccountRow
	if err := s.db.Raw(`
		SELECT accounts.username, accounts.domain
		FROM blocks
		INNER JOIN accounts ON accounts.id = blocks.target_account_id
		WHERE blocks.account_id = ?
		ORDER BY blocks.id DESC
	`, accountID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return csvBytes("blocks.csv", nil, func(w *csv.Writer) error {
		for _, row := range rows {
			if err := w.Write([]string{s.exportAccountAddress(row)}); err != nil {
				return err
			}
		}
		return nil
	}), nil
}

func (s *Server) exportMutesCSV(c *echo.Context) error {
	account, _, _, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	body, err := s.exportMutesCSVBytes(account.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="mutes.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", body)
}

func (s *Server) exportMutesCSVBytes(accountID int64) ([]byte, error) {
	var rows []exportMuteRow
	if err := s.db.Raw(`
		SELECT accounts.username, accounts.domain, mutes.hide_notifications
		FROM mutes
		INNER JOIN accounts ON accounts.id = mutes.target_account_id
		WHERE mutes.account_id = ?
		ORDER BY mutes.id DESC
	`, accountID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return csvBytes("mutes.csv", [][]string{
		{"Account address", "Hide notifications"},
	}, func(w *csv.Writer) error {
		for _, row := range rows {
			if err := w.Write([]string{s.exportAccountAddress(exportAccountRow{Username: row.Username, Domain: row.Domain}), strconv.FormatBool(row.HideNotifications)}); err != nil {
				return err
			}
		}
		return nil
	}), nil
}

func (s *Server) exportListsCSV(c *echo.Context) error {
	account, _, _, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	body, err := s.exportListsCSVBytes(account.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="lists.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", body)
}

func (s *Server) exportListsCSVBytes(accountID int64) ([]byte, error) {
	var rows []exportListRow
	if err := s.db.Raw(`
		SELECT lists.title, accounts.username, accounts.domain
		FROM lists
		INNER JOIN list_accounts ON list_accounts.list_id = lists.id
		INNER JOIN accounts ON accounts.id = list_accounts.account_id
		WHERE lists.account_id = ?
		ORDER BY lists.id ASC, list_accounts.id ASC
	`, accountID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return csvBytes("lists.csv", nil, func(w *csv.Writer) error {
		for _, row := range rows {
			if err := w.Write([]string{row.Title, s.exportAccountAddress(exportAccountRow{Username: row.Username, Domain: row.Domain})}); err != nil {
				return err
			}
		}
		return nil
	}), nil
}

func (s *Server) exportDomainBlocksCSV(c *echo.Context) error {
	account, _, _, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	body, err := s.exportDomainBlocksCSVBytes(account.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="domain_blocks.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", body)
}

func (s *Server) exportDomainBlocksCSVBytes(accountID int64) ([]byte, error) {
	var rows []exportDomainBlockRow
	if err := s.db.Raw(`
		SELECT domain
		FROM account_domain_blocks
		WHERE account_id = ?
		ORDER BY id ASC
	`, accountID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return csvBytes("domain_blocks.csv", nil, func(w *csv.Writer) error {
		for _, row := range rows {
			if err := w.Write([]string{accountDomainBlockDisplayDomain(row.Domain)}); err != nil {
				return err
			}
		}
		return nil
	}), nil
}

func (s *Server) exportBookmarksCSV(c *echo.Context) error {
	account, _, _, err := s.currentAccountForWeb(c)
	if err != nil {
		return redirectToSignIn(c)
	}
	body, err := s.exportBookmarksCSVBytes(account.ID)
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Disposition", `attachment; filename="bookmarks.csv"`)
	return c.Blob(http.StatusOK, "text/csv; charset=utf-8", body)
}

func (s *Server) exportBookmarksCSVBytes(accountID int64) ([]byte, error) {
	var rows []exportBookmarkRow
	if err := s.db.Raw(`
		SELECT statuses.id AS status_id, statuses.uri AS status_uri, statuses.url AS status_url,
		       accounts.id AS account_id, accounts.username AS account_username, accounts.domain AS account_domain,
		       accounts.id_scheme AS account_id_scheme
		FROM bookmarks
		INNER JOIN statuses ON statuses.id = bookmarks.status_id
		INNER JOIN accounts ON accounts.id = statuses.account_id
		WHERE bookmarks.account_id = ?
		ORDER BY bookmarks.id DESC
	`, accountID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return csvBytes("bookmarks.csv", nil, func(w *csv.Writer) error {
		for _, row := range rows {
			if err := w.Write([]string{s.exportStatusURI(row)}); err != nil {
				return err
			}
		}
		return nil
	}), nil
}

func (s *Server) exportAccountAddress(row exportAccountRow) string {
	if row.Domain.Valid && row.Domain.String != "" {
		return row.Username + "@" + row.Domain.String
	}
	return row.Username + "@" + s.cfg.LocalDomain
}

func (s *Server) exportStatusURI(row exportBookmarkRow) string {
	if row.AccountDomain.Valid && row.AccountDomain.String != "" {
		if row.StatusURI.Valid && row.StatusURI.String != "" {
			return row.StatusURI.String
		}
		if row.StatusURL.Valid && row.StatusURL.String != "" {
			return row.StatusURL.String
		}
		return "https://" + row.AccountDomain.String + "/users/" + url.PathEscape(row.AccountUsername) + "/statuses/" + strconv.FormatInt(row.StatusID, 10)
	}
	account := models.Account{ID: row.AccountID, Username: row.AccountUsername, IDScheme: row.AccountIDScheme}
	return activityPubStatusURL(s, account, row.StatusID)
}

func csvBytes(filename string, header [][]string, writeRows func(*csv.Writer) error, contexts ...*echo.Context) []byte {
	for _, c := range contexts {
		if c != nil {
			c.Response().Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
			break
		}
	}
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	for _, row := range header {
		_ = writer.Write(row)
	}
	if writeRows != nil {
		_ = writeRows(writer)
	}
	writer.Flush()
	return buf.Bytes()
}

func redirectToSignIn(c *echo.Context) error {
	return c.Redirect(http.StatusFound, "/auth/sign_in?redirect_to="+url.QueryEscape(c.Request().URL.RequestURI()))
}
