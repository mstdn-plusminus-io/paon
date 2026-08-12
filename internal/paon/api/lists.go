package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

var errListAccountDuplicate = errors.New("list account duplicate")

type listParams struct {
	Title                *string
	RepliesPolicy        *int
	Exclusive            *bool
	InvalidRepliesPolicy bool
}

func (s *Server) lists(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:lists")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")

	var rows []models.List
	if err := s.db.Where("account_id = ?", account.ID).Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializeLists(rows))
}

func (s *Server) showList(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "read", "read:lists")
	if err != nil {
		return err
	}
	c.Response().Header().Set("Vary", "Authorization")
	list, err := s.findOwnedList(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	return c.JSON(http.StatusOK, serializer.ListFromModel(*list))
}

func (s *Server) createList(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:lists")
	if err != nil {
		return err
	}
	params := readListParams(c)
	if params.InvalidRepliesPolicy {
		return apiError(c, http.StatusUnprocessableEntity, "'replies_policy' is not a valid replies_policy")
	}
	title := ""
	if params.Title != nil {
		title = *params.Title
	}
	if strings.TrimSpace(title) == "" {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Title can't be blank")
	}
	if !validListTitle(title) {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Title is too long (maximum is 256 characters)")
	}

	var count int64
	if err := s.db.Model(&models.List{}).Where("account_id = ?", account.ID).Count(&count).Error; err != nil {
		return err
	}
	if count >= 50 {
		return apiError(c, http.StatusUnprocessableEntity, "Validation failed: You have reached the list limit")
	}

	now := time.Now().UTC()
	list := models.List{
		AccountID:     account.ID,
		Title:         title,
		CreatedAt:     now,
		UpdatedAt:     now,
		RepliesPolicy: 0,
		Exclusive:     false,
	}
	if params.RepliesPolicy != nil {
		list.RepliesPolicy = *params.RepliesPolicy
	}
	if params.Exclusive != nil {
		list.Exclusive = *params.Exclusive
	}
	if err := s.db.Create(&list).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.ListFromModel(list))
}

func (s *Server) updateList(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:lists")
	if err != nil {
		return err
	}
	list, err := s.findOwnedList(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}

	params := readListParams(c)
	if params.InvalidRepliesPolicy {
		return apiError(c, http.StatusUnprocessableEntity, "'replies_policy' is not a valid replies_policy")
	}
	updates := map[string]any{"updated_at": time.Now().UTC()}
	if params.Title != nil {
		title := *params.Title
		if strings.TrimSpace(title) == "" {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Title can't be blank")
		}
		if !validListTitle(title) {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Title is too long (maximum is 256 characters)")
		}
		updates["title"] = title
	}
	if params.RepliesPolicy != nil {
		updates["replies_policy"] = *params.RepliesPolicy
	}
	if params.Exclusive != nil {
		updates["exclusive"] = *params.Exclusive
	}
	if err := s.db.Model(list).Updates(updates).Error; err != nil {
		return err
	}
	if err := s.db.Where("id = ?", list.ID).First(list).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.ListFromModel(*list))
}

func validListTitle(title string) bool {
	return strings.TrimSpace(title) != "" && utf8.RuneCountInString(title) <= 256
}

func (s *Server) deleteList(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:lists")
	if err != nil {
		return err
	}
	list, err := s.findOwnedList(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("list_id = ?", list.ID).Delete(&models.ListAccount{}).Error; err != nil {
			return err
		}
		return tx.Delete(list).Error
	}); err != nil {
		return err
	}
	s.clearListFeedCache(list.ID)
	return renderEmpty(c)
}

func (s *Server) listAccounts(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "read", "read:lists")
	if err != nil {
		return err
	}
	list, err := s.findOwnedList(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}

	query := s.db.Model(&models.ListAccount{}).
		Preload("Account.AccountStat").
		Preload("Account.User.Role").
		Joins("JOIN accounts ON accounts.id = list_accounts.account_id").
		Where("list_accounts.list_id = ? AND accounts.suspended_at IS NULL", list.ID)
	unlimited := c.QueryParam("limit") == "0"
	if !unlimited {
		if maxID := c.QueryParam("max_id"); queryParamValuePresent(c, "max_id") {
			query = query.Where("accounts.id < ?", maxID)
		}
		if sinceID := c.QueryParam("since_id"); queryParamValuePresent(c, "since_id") {
			query = query.Where("accounts.id > ?", sinceID)
		}
		query = query.Order("accounts.id DESC")
	}
	limitValue := limit(c, 40, 80)
	if !unlimited {
		query = query.Limit(limitValue)
	}

	var rows []models.ListAccount
	if err := query.Find(&rows).Error; err != nil {
		return err
	}
	accounts := make([]models.Account, 0, len(rows))
	for _, row := range rows {
		accounts = append(accounts, row.Account)
	}
	if !unlimited && len(accounts) > 0 {
		c.Response().Header().Set("Link", limitOnlyPaginationLink(c, accounts[0].ID, accounts[len(accounts)-1].ID, "since_id", len(accounts) == limitValue))
	}
	return c.JSON(http.StatusOK, serializeAccounts(s.cfg, accounts))
}

func (s *Server) addListAccounts(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:lists")
	if err != nil {
		return err
	}
	list, err := s.findOwnedList(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	accountIDs, invalidAccountIDs := listAccountIDs(c)
	if invalidAccountIDs {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if len(accountIDs) == 0 {
		return renderEmpty(c)
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		for _, targetID := range accountIDs {
			item, err := listAccountRow(tx, *list, targetID)
			if err != nil {
				return err
			}
			if err := tx.Create(item).Error; err != nil {
				if isUniqueConstraintError(err) {
					return errListAccountDuplicate
				}
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apiError(c, http.StatusNotFound, "Record not found")
		}
		if err == errListAccountDuplicate {
			return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Account has already been taken")
		}
		return err
	}
	return renderEmpty(c)
}

func (s *Server) removeListAccounts(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:lists")
	if err != nil {
		return err
	}
	list, err := s.findOwnedList(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	accountIDs, invalidAccountIDs := listAccountIDs(c)
	if invalidAccountIDs {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if len(accountIDs) == 0 {
		return renderEmpty(c)
	}
	if err := s.db.Where("list_id = ? AND account_id IN ?", list.ID, accountIDs).Delete(&models.ListAccount{}).Error; err != nil {
		return err
	}
	return renderEmpty(c)
}

func (s *Server) listTimeline(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "read", "read:lists")
	if err != nil {
		return err
	}
	list, err := s.findOwnedList(account.ID, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	query := s.listTimelineQuery(*list)
	return s.statusList(c, query)
}

func (s *Server) accountLists(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	account, _, err := s.requireAccountScope(c, "read", "read:lists")
	if err != nil {
		return err
	}
	target, err := s.findAccountByID(c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if target.SuspendedAt.Valid {
		return c.JSON(http.StatusOK, []serializer.List{})
	}

	var rows []models.List
	if err := s.db.Model(&models.List{}).
		Joins("JOIN list_accounts ON list_accounts.list_id = lists.id").
		Where("lists.account_id = ? AND list_accounts.account_id = ?", account.ID, target.ID).
		Order("lists.id ASC").
		Find(&rows).Error; err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializeLists(rows))
}

func (s *Server) listTimelineQuery(list models.List) *gorm.DB {
	query := s.statusQuery().
		Joins("JOIN list_accounts ON list_accounts.account_id = statuses.account_id").
		Where("list_accounts.list_id = ?", list.ID).
		Where("(list_accounts.follow_id IS NOT NULL OR list_accounts.account_id = ?)", list.AccountID).
		Where("statuses.deleted_at IS NULL").
		Where("statuses.reblog_of_id IS NULL OR EXISTS (SELECT 1 FROM statuses reblogged_statuses WHERE reblogged_statuses.id = statuses.reblog_of_id AND reblogged_statuses.deleted_at IS NULL)").
		Where("statuses.visibility IN ?", []int{0, 1, 2})

	switch list.RepliesPolicy {
	case 2:
		query = query.Where("(statuses.reply = false OR statuses.in_reply_to_account_id = statuses.account_id)")
	case 1:
		query = query.Where("1 = 1")
	default:
		query = query.Where(`(
			statuses.reply = false
			OR statuses.in_reply_to_account_id = statuses.account_id
			OR EXISTS (
				SELECT 1 FROM list_accounts reply_members
				WHERE reply_members.list_id = ?
				  AND reply_members.account_id = statuses.in_reply_to_account_id
			)
		)`, list.ID)
	}
	return query
}

func (s *Server) findOwnedList(accountID int64, id string) (*models.List, error) {
	var list models.List
	err := s.db.Where("id = ? AND account_id = ?", id, accountID).First(&list).Error
	return &list, err
}

func listAccountRow(tx *gorm.DB, list models.List, targetID int64) (*models.ListAccount, error) {
	var account models.Account
	if err := tx.Where("id = ?", targetID).First(&account).Error; err != nil {
		return nil, err
	}
	item := &models.ListAccount{ListID: list.ID, AccountID: account.ID}
	if account.ID == list.AccountID {
		return item, nil
	}

	var follow models.Follow
	if err := tx.Where("account_id = ? AND target_account_id = ?", list.AccountID, account.ID).First(&follow).Error; err == nil {
		item.FollowID = sql.NullInt64{Int64: follow.ID, Valid: true}
		return item, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var request models.FollowRequest
	if err := tx.Where("account_id = ? AND target_account_id = ?", list.AccountID, account.ID).First(&request).Error; err == nil {
		item.FollowRequestID = sql.NullInt64{Int64: request.ID, Valid: true}
		return item, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return nil, gorm.ErrRecordNotFound
}

func serializeLists(lists []models.List) []serializer.List {
	out := make([]serializer.List, 0, len(lists))
	for _, list := range lists {
		out = append(out, serializer.ListFromModel(list))
	}
	return out
}

func readListParams(c *echo.Context) listParams {
	out := listParams{}
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "application/json") {
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(c.Request().Body).Decode(&raw); err == nil {
			if value, ok := raw["title"]; ok {
				var title string
				if string(value) == "null" {
					out.Title = &title
				} else if json.Unmarshal(value, &title) == nil {
					out.Title = &title
				}
			}
			if value, ok := raw["replies_policy"]; ok {
				if policy, ok := parseRepliesPolicyRaw(value); ok {
					out.RepliesPolicy = &policy
				} else {
					out.InvalidRepliesPolicy = true
				}
			}
			if value, ok := raw["exclusive"]; ok {
				if exclusive, ok := parseBoolRaw(value); ok {
					out.Exclusive = &exclusive
				}
			}
			return out
		}
	}

	if value, ok := formField(c, "title"); ok {
		out.Title = &value
	}
	if value := c.FormValue("replies_policy"); value != "" {
		if policy, ok := parseRepliesPolicy(value); ok {
			out.RepliesPolicy = &policy
		} else {
			out.InvalidRepliesPolicy = true
		}
	}
	values, _ := c.FormValues()
	if _, ok := values["exclusive"]; ok {
		exclusive := truthy(lastFormValue(values, "exclusive"))
		out.Exclusive = &exclusive
	}
	return out
}

func parseRepliesPolicyRaw(raw json.RawMessage) (int, bool) {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return parseRepliesPolicy(value)
	}
	var number int
	if json.Unmarshal(raw, &number) == nil {
		if number >= 0 && number <= 2 {
			return number, true
		}
	}
	return 0, false
}

func parseBoolRaw(raw json.RawMessage) (bool, bool) {
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value, true
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return truthy(text), true
	}
	return false, false
}

func parseRepliesPolicy(value string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "list":
		return 0, true
	case "followed":
		return 1, true
	case "none":
		return 2, true
	default:
		number, err := strconv.Atoi(value)
		if err == nil && number >= 0 && number <= 2 {
			return number, true
		}
		return 0, false
	}
}

func listAccountIDs(c *echo.Context) ([]int64, bool) {
	values := append([]string{}, c.QueryParams()["account_ids[]"]...)
	if len(values) == 0 && strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "application/json") {
		var body struct {
			AccountIDs []json.RawMessage `json:"account_ids"`
		}
		if err := json.NewDecoder(c.Request().Body).Decode(&body); err == nil {
			for _, raw := range body.AccountIDs {
				var text string
				if json.Unmarshal(raw, &text) == nil {
					values = append(values, text)
					continue
				}
				var number int64
				if json.Unmarshal(raw, &number) == nil {
					values = append(values, strconv.FormatInt(number, 10))
				}
			}
		}
	}
	if len(values) == 0 {
		_ = c.Request().ParseForm()
		values = append(values, c.Request().PostForm["account_ids[]"]...)
	}
	return parseIDValues(values)
}

func parseIDValues(values []string) ([]int64, bool) {
	out := []int64{}
	seen := map[int64]struct{}{}
	for _, value := range values {
		id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return out, true
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, false
}
