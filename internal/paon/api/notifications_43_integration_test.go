//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestNotification43GroupingPolicyRequestsAndWarningsAgainstPostgresAndRedis(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	redisURL := os.Getenv("PAON_TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL and PAON_TEST_REDIS_URL are required for integration tests")
	}
	cfg := config.Config{
		DatabaseURL:          databaseURL,
		DatabaseMaxOpenConns: 24,
		DatabaseMaxIdleConns: 8,
		RedisURL:             redisURL,
		RedisNamespace:       "paon:integration:notifications43:" + randomHex(8) + ":",
		LocalDomain:          "example.test",
		WebDomain:            "example.test",
		SecretKeyBase:        "integration-secret",
	}
	database, err := paondb.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`).Error; err != nil {
		t.Fatal(err)
	}
	if applied, err := migrate.Run(context.Background(), database); err != nil || !applied {
		t.Fatalf("migrate = %v, %v", applied, err)
	}
	server := &Server{cfg: cfg, db: database}
	t.Cleanup(func() { deleteNotificationIntegrationRedisNamespace(server) })

	base := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	recipient := createNotificationIntegrationAccount(t, database, "recipient", "", base.Add(-365*24*time.Hour))
	createNotificationIntegrationUser(t, database, recipient.ID, "recipient@example.test")
	token := createNotificationIntegrationToken(t, database, recipient.ID, "notification-owner-token")

	t.Run("concurrent group creation keeps the first bucket and cursor pages stay bounded", func(t *testing.T) {
		setNotificationIntegrationPolicy(t, database, recipient.ID, 0)
		firstSender := createNotificationIntegrationAccount(t, database, "group-first", "remote.example", base.Add(-90*24*time.Hour))
		first, err := createNotificationIntegrationRow(server, recipient.ID, firstSender.ID, 10_001, "Follow", "follow", base)
		if err != nil || first == nil {
			t.Fatalf("create first grouped notification = %#v, %v", first, err)
		}
		firstBucket := base.Unix() / int64(time.Hour/time.Second)
		wantFirstKey := "follow-" + strconv.FormatInt(firstBucket, 10)
		if !first.GroupKey.Valid || first.GroupKey.String != wantFirstKey {
			t.Fatalf("first group key = %#v, want %q", first.GroupKey, wantFirstKey)
		}

		const workers = 12
		senders := make([]models.Account, 0, workers)
		for i := range workers {
			senders = append(senders, createNotificationIntegrationAccount(t, database, "group-concurrent-"+strconv.Itoa(i), "remote.example", base.Add(-90*24*time.Hour)))
		}
		start := make(chan struct{})
		rows := make(chan *models.Notification, workers)
		errs := make(chan error, workers)
		var group sync.WaitGroup
		for i := range workers {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				<-start
				row, createErr := createNotificationIntegrationRow(server, recipient.ID, senders[index].ID, int64(10_100+index), "Follow", "follow", base.Add(11*time.Hour))
				rows <- row
				errs <- createErr
			}(i)
		}
		close(start)
		group.Wait()
		close(rows)
		close(errs)
		for createErr := range errs {
			if createErr != nil {
				t.Fatalf("concurrent grouped notification: %v", createErr)
			}
		}
		for row := range rows {
			if row == nil || !row.GroupKey.Valid || row.GroupKey.String != wantFirstKey {
				t.Fatalf("concurrent group row = %#v, want group key %q", row, wantFirstKey)
			}
		}

		boundarySender := createNotificationIntegrationAccount(t, database, "group-boundary", "remote.example", base.Add(-90*24*time.Hour))
		boundary, err := createNotificationIntegrationRow(server, recipient.ID, boundarySender.ID, 10_999, "Follow", "follow", base.Add(12*time.Hour))
		if err != nil || boundary == nil {
			t.Fatalf("create boundary notification = %#v, %v", boundary, err)
		}
		wantBoundaryKey := "follow-" + strconv.FormatInt(firstBucket+12, 10)
		if !boundary.GroupKey.Valid || boundary.GroupKey.String != wantBoundaryKey {
			t.Fatalf("boundary group key = %#v, want %q", boundary.GroupKey, wantBoundaryKey)
		}

		ungrouped := models.Notification{
			ActivityID: 20_001, ActivityType: "Status", Type: "update", AccountID: recipient.ID, FromAccountID: firstSender.ID,
			CreatedAt: base.Add(13 * time.Hour), UpdatedAt: base.Add(13 * time.Hour),
		}
		if err := database.Create(&ungrouped).Error; err != nil {
			t.Fatal(err)
		}
		firstPage, firstLink := requestNotificationIntegrationGroups(t, server, token, "limit=2")
		if got, want := notificationIntegrationGroupKeys(firstPage), []string{"ungrouped-" + strconv.FormatInt(ungrouped.ID, 10), wantBoundaryKey}; !equalStrings(got, want) {
			t.Fatalf("first page group keys = %#v, want %#v", got, want)
		}
		if firstLink == "" || !notificationIntegrationContainsAll(firstLink, "max_id="+strconv.FormatInt(boundary.ID, 10), "min_id="+strconv.FormatInt(ungrouped.ID, 10)) {
			t.Fatalf("first page Link = %q", firstLink)
		}
		if firstPage[1].PageMinID == nil || firstPage[1].PageMaxID == nil || firstPage[1].LatestPageNotificationAt == nil {
			t.Fatalf("boundary group is missing pagination metadata: %#v", firstPage[1])
		}

		insertedBetweenPages := models.Notification{
			ActivityID: 20_002, ActivityType: "Status", Type: "update", AccountID: recipient.ID, FromAccountID: firstSender.ID,
			CreatedAt: base.Add(14 * time.Hour), UpdatedAt: base.Add(14 * time.Hour),
		}
		if err := database.Create(&insertedBetweenPages).Error; err != nil {
			t.Fatal(err)
		}
		secondPage, _ := requestNotificationIntegrationGroups(t, server, token, "limit=2&max_id="+strconv.FormatInt(boundary.ID, 10))
		if got, want := notificationIntegrationGroupKeys(secondPage), []string{wantFirstKey}; !equalStrings(got, want) {
			t.Fatalf("second page after concurrent insert = %#v, want %#v", got, want)
		}
		if secondPage[0].NotificationsCount != workers+1 {
			t.Fatalf("first bucket notifications_count = %d, want %d", secondPage[0].NotificationsCount, workers+1)
		}
	})

	t.Run("policy actions permission override and hard drops use durable rows", func(t *testing.T) {
		policySenders := make([]models.Account, 0, 4)
		for i := range 4 {
			policySenders = append(policySenders, createNotificationIntegrationAccount(t, database, "policy-"+strconv.Itoa(i), "policy.example", base.Add(-90*24*time.Hour)))
		}
		setNotificationIntegrationPolicy(t, database, recipient.ID, 0)
		accepted, err := createNotificationIntegrationRow(server, recipient.ID, policySenders[0].ID, 30_001, "Follow", "follow", base.Add(20*time.Hour))
		if err != nil || accepted == nil || accepted.Filtered {
			t.Fatalf("accept policy notification = %#v, %v", accepted, err)
		}

		setNotificationIntegrationPolicy(t, database, recipient.ID, 1)
		filtered, err := createNotificationIntegrationRow(server, recipient.ID, policySenders[1].ID, 30_002, "Follow", "follow", base.Add(20*time.Hour))
		if err != nil || filtered == nil || !filtered.Filtered || filtered.GroupKey.Valid {
			t.Fatalf("filter policy notification = %#v, %v", filtered, err)
		}

		setNotificationIntegrationPolicy(t, database, recipient.ID, 2)
		dropped, err := createNotificationIntegrationRow(server, recipient.ID, policySenders[2].ID, 30_003, "Follow", "follow", base.Add(20*time.Hour))
		if err != nil || dropped != nil {
			t.Fatalf("drop policy notification = %#v, %v", dropped, err)
		}
		var droppedCount int64
		if err := database.Model(&models.Notification{}).Where("account_id = ? AND activity_id = ?", recipient.ID, 30_003).Count(&droppedCount).Error; err != nil || droppedCount != 0 {
			t.Fatalf("persisted dropped notifications = %d, %v", droppedCount, err)
		}

		now := time.Now().UTC()
		permission := models.NotificationPermission{AccountID: recipient.ID, FromAccountID: policySenders[3].ID, CreatedAt: now, UpdatedAt: now}
		if err := database.Create(&permission).Error; err != nil {
			t.Fatal(err)
		}
		overridden, err := createNotificationIntegrationRow(server, recipient.ID, policySenders[3].ID, 30_004, "Follow", "follow", base.Add(20*time.Hour))
		if err != nil || overridden == nil || overridden.Filtered {
			t.Fatalf("permission override notification = %#v, %v", overridden, err)
		}
		block := models.Block{AccountID: recipient.ID, TargetAccountID: policySenders[3].ID, CreatedAt: now, UpdatedAt: now}
		if err := database.Create(&block).Error; err != nil {
			t.Fatal(err)
		}
		hardDropped, err := createNotificationIntegrationRow(server, recipient.ID, policySenders[3].ID, 30_005, "Follow", "follow", base.Add(20*time.Hour))
		if err != nil || hardDropped != nil {
			t.Fatalf("permission bypassed block hard drop: %#v, %v", hardDropped, err)
		}
	})

	t.Run("request accept and dismiss races are idempotent and merged publishes once", func(t *testing.T) {
		setNotificationIntegrationPolicy(t, database, recipient.ID, 1)
		acceptSender := createNotificationIntegrationAccount(t, database, "request-accept", "requests.example", base.Add(-90*24*time.Hour))
		acceptNotification := createFilteredMentionNotificationIntegration(t, server, recipient, acceptSender, base.Add(21*time.Hour), "accept me")
		var acceptRequest models.NotificationRequest
		if err := database.Where("account_id = ? AND from_account_id = ?", recipient.ID, acceptSender.ID).First(&acceptRequest).Error; err != nil {
			t.Fatalf("load filtered notification request: %v", err)
		}

		events, stopSubscription := subscribeNotificationIntegrationEvents(t, server, recipient.ID)
		statuses := runNotificationIntegrationRequestRace(server, token, acceptRequest.ID, true)
		if !equalInts(statuses, []int{http.StatusOK, http.StatusNotFound}) {
			t.Fatalf("accept race statuses = %#v, want [200 404]", statuses)
		}
		var permissionCount, requestCount int64
		if err := database.Model(&models.NotificationPermission{}).Where("account_id = ? AND from_account_id = ?", recipient.ID, acceptSender.ID).Count(&permissionCount).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Model(&models.NotificationRequest{}).Where("id = ?", acceptRequest.ID).Count(&requestCount).Error; err != nil {
			t.Fatal(err)
		}
		var acceptedRow models.Notification
		if err := database.First(&acceptedRow, acceptNotification.ID).Error; err != nil {
			t.Fatal(err)
		}
		if permissionCount != 1 || requestCount != 0 || acceptedRow.Filtered {
			t.Fatalf("accept durable state: permissions=%d requests=%d filtered=%v", permissionCount, requestCount, acceptedRow.Filtered)
		}
		merged := waitForNotificationIntegrationMergedEvents(t, events, 350*time.Millisecond)
		stopSubscription()
		if len(merged) != 1 || string(merged[0].Payload) != `"1"` {
			t.Fatalf("notifications_merged events = %#v, want one payload string \"1\"", merged)
		}

		dismissSender := createNotificationIntegrationAccount(t, database, "request-dismiss", "requests.example", base.Add(-90*24*time.Hour))
		dismissNotification := createFilteredMentionNotificationIntegration(t, server, recipient, dismissSender, base.Add(22*time.Hour), "dismiss me")
		var dismissRequest models.NotificationRequest
		if err := database.Where("account_id = ? AND from_account_id = ?", recipient.ID, dismissSender.ID).First(&dismissRequest).Error; err != nil {
			t.Fatal(err)
		}
		statuses = runNotificationIntegrationRequestRace(server, token, dismissRequest.ID, false)
		if !equalInts(statuses, []int{http.StatusOK, http.StatusNotFound}) {
			t.Fatalf("dismiss race statuses = %#v, want [200 404]", statuses)
		}
		var dismissedNotificationCount, dismissRequestCount, dismissPermissionCount int64
		if err := database.Model(&models.Notification{}).Where("id = ?", dismissNotification.ID).Count(&dismissedNotificationCount).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Model(&models.NotificationRequest{}).Where("id = ?", dismissRequest.ID).Count(&dismissRequestCount).Error; err != nil {
			t.Fatal(err)
		}
		if err := database.Model(&models.NotificationPermission{}).Where("account_id = ? AND from_account_id = ?", recipient.ID, dismissSender.ID).Count(&dismissPermissionCount).Error; err != nil {
			t.Fatal(err)
		}
		if dismissedNotificationCount != 0 || dismissRequestCount != 0 || dismissPermissionCount != 0 {
			t.Fatalf("dismiss durable state: notifications=%d requests=%d permissions=%d", dismissedNotificationCount, dismissRequestCount, dismissPermissionCount)
		}
	})

	t.Run("moderation warnings ignore drop policy and retry exactly once", func(t *testing.T) {
		setNotificationIntegrationPolicy(t, database, recipient.ID, 2)
		moderator := createNotificationIntegrationAccount(t, database, "moderator", "", base.Add(-365*24*time.Hour))
		now := time.Now().UTC()
		warning := models.AccountWarning{
			AccountID: models.AccountWarningAccountID(moderator.ID), TargetAccountID: models.AccountWarningTargetAccountID(recipient.ID),
			Action: 1500, Text: "remove unsafe status", StatusIDs: models.StringArray{"999999"}, CreatedAt: now, UpdatedAt: now,
		}
		if err := database.Create(&warning).Error; err != nil {
			t.Fatal(err)
		}
		const retries = 8
		start := make(chan struct{})
		errs := make(chan error, retries)
		var group sync.WaitGroup
		for range retries {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				errs <- database.Transaction(func(tx *gorm.DB) error {
					return createModerationWarningNotification(tx, warning, now)
				})
			}()
		}
		close(start)
		group.Wait()
		close(errs)
		for createErr := range errs {
			if createErr != nil {
				t.Fatalf("concurrent moderation warning retry: %v", createErr)
			}
		}
		var warnings []models.Notification
		if err := database.Where("account_id = ? AND activity_type = ? AND activity_id = ? AND type = ?", recipient.ID, "AccountWarning", warning.ID, "moderation_warning").Find(&warnings).Error; err != nil {
			t.Fatal(err)
		}
		if len(warnings) != 1 {
			t.Fatalf("moderation warning notification count = %d, want 1", len(warnings))
		}
		if warnings[0].Filtered || warnings[0].GroupKey.Valid {
			t.Fatalf("moderation warning flags = filtered %v, group key %#v; want unfiltered and ungrouped", warnings[0].Filtered, warnings[0].GroupKey)
		}
		groupEntity := requestNotificationIntegrationGroup(t, server, token, "ungrouped-"+strconv.FormatInt(warnings[0].ID, 10))
		if groupEntity.Type != "moderation_warning" || groupEntity.ModerationWarning == nil || groupEntity.ModerationWarning.Action != "delete_statuses" {
			t.Fatalf("serialized moderation warning group = %#v", groupEntity)
		}
	})
}

func createNotificationIntegrationAccount(t *testing.T, database *gorm.DB, username string, domain string, createdAt time.Time) models.Account {
	t.Helper()
	account := models.Account{Username: username, CreatedAt: createdAt, UpdatedAt: createdAt}
	if domain != "" {
		account.Domain = sql.NullString{String: domain, Valid: true}
		account.URI = "https://" + domain + "/users/" + username
		account.URL = sql.NullString{String: account.URI, Valid: true}
	}
	if err := database.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	stat := models.AccountStat{AccountID: account.ID, CreatedAt: createdAt, UpdatedAt: createdAt}
	if err := database.Create(&stat).Error; err != nil {
		t.Fatal(err)
	}
	return account
}

func createNotificationIntegrationUser(t *testing.T, database *gorm.DB, accountID int64, email string) models.User {
	t.Helper()
	now := time.Now().UTC()
	user := models.User{AccountID: accountID, Email: email, Approved: true, ConfirmedAt: sql.NullTime{Time: now, Valid: true}, CreatedAt: now, UpdatedAt: now}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return user
}

func createNotificationIntegrationToken(t *testing.T, database *gorm.DB, accountID int64, token string) string {
	t.Helper()
	var user models.User
	if err := database.Where("account_id = ?", accountID).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	accessToken := models.OAuthAccessToken{
		Token: token, Scopes: "read write read:notifications write:notifications", CreatedAt: time.Now().UTC(),
		ResourceOwnerID: sql.NullInt64{Int64: user.ID, Valid: true},
	}
	if err := database.Create(&accessToken).Error; err != nil {
		t.Fatal(err)
	}
	return token
}

func setNotificationIntegrationPolicy(t *testing.T, database *gorm.DB, accountID int64, action int) {
	t.Helper()
	now := time.Now().UTC()
	policy := models.NotificationPolicy{AccountID: accountID}
	err := database.Where("account_id = ?", accountID).First(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		policy = models.NotificationPolicy{AccountID: accountID, CreatedAt: now}
	} else if err != nil {
		t.Fatal(err)
	}
	policy.ForNotFollowing = action
	policy.ForNotFollowers = action
	policy.ForNewAccounts = action
	policy.ForPrivateMentions = action
	policy.ForLimitedAccounts = action
	policy.UpdatedAt = now
	if policy.ID == 0 {
		err = database.Create(&policy).Error
	} else {
		err = database.Save(&policy).Error
	}
	if err != nil {
		t.Fatal(err)
	}
}

func createNotificationIntegrationRow(server *Server, accountID int64, fromAccountID int64, activityID int64, activityType string, kind string, at time.Time) (*models.Notification, error) {
	var notification *models.Notification
	err := server.db.Transaction(func(tx *gorm.DB) error {
		row, err := createRelationshipNotificationRow(tx, accountID, fromAccountID, activityID, activityType, kind, at)
		if err != nil || row == nil {
			notification = row
			return err
		}
		if err := server.applyRelationshipNotificationRedisGroupKey(tx, row); err != nil {
			return err
		}
		notification = row
		return nil
	})
	return notification, err
}

func requestNotificationIntegrationGroups(t *testing.T, server *Server, token string, rawQuery string) ([]notificationGroupEntity, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/notifications?"+rawQuery, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	if err := server.groupedNotifications(c); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("grouped notifications status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		NotificationGroups []notificationGroupEntity `json:"notification_groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.NotificationGroups, rec.Header().Get("Link")
}

func requestNotificationIntegrationGroup(t *testing.T, server *Server, token string, groupKey string) notificationGroupEntity {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v2/notifications/"+groupKey, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	c.SetPathValues(echo.PathValues{{Name: "group_key", Value: groupKey}})
	if err := server.showGroupedNotification(c); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		NotificationGroups []notificationGroupEntity `json:"notification_groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.NotificationGroups) != 1 {
		t.Fatalf("notification groups = %#v", envelope.NotificationGroups)
	}
	return envelope.NotificationGroups[0]
}

func createFilteredMentionNotificationIntegration(t *testing.T, server *Server, recipient models.Account, sender models.Account, at time.Time, text string) models.Notification {
	t.Helper()
	status := models.Status{AccountID: sender.ID, Text: text, Visibility: 0, Local: sql.NullBool{Bool: false, Valid: true}, CreatedAt: at, UpdatedAt: at}
	if err := server.db.Create(&status).Error; err != nil {
		t.Fatal(err)
	}
	mention := models.Mention{StatusID: models.MentionStatusID(status.ID), AccountID: models.MentionAccountID(recipient.ID), CreatedAt: at, UpdatedAt: at}
	if err := server.db.Create(&mention).Error; err != nil {
		t.Fatal(err)
	}
	notification, err := createNotificationIntegrationRow(server, recipient.ID, sender.ID, mention.ID, "Mention", "mention", at)
	if err != nil || notification == nil || !notification.Filtered {
		t.Fatalf("filtered mention notification = %#v, %v", notification, err)
	}
	return *notification
}

func runNotificationIntegrationRequestRace(server *Server, token string, requestID int64, accept bool) []int {
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			req := httptest.NewRequest(http.MethodPost, "/api/v1/notifications/requests/"+strconv.FormatInt(requestID, 10), nil)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			c := echo.NewContext(req, rec, echo.New())
			c.SetPathValues(echo.PathValues{{Name: "id", Value: strconv.FormatInt(requestID, 10)}})
			var err error
			if accept {
				err = server.acceptNotificationRequest(c)
			} else {
				err = server.dismissNotificationRequest(c)
			}
			if err == nil {
				statuses <- rec.Code
				return
			}
			if apiErrorStatus(err, http.StatusNotFound) {
				statuses <- http.StatusNotFound
				return
			}
			statuses <- 0
		}()
	}
	close(start)
	group.Wait()
	close(statuses)
	out := make([]int, 0, 2)
	for status := range statuses {
		out = append(out, status)
	}
	sort.Ints(out)
	return out
}

func subscribeNotificationIntegrationEvents(t *testing.T, server *Server, accountID int64) (<-chan redisMessage, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan redisMessage, 16)
	errs := make(chan error, 1)
	logicalChannel := "timeline:" + strconv.FormatInt(accountID, 10) + ":notifications"
	go func() {
		errs <- server.subscribeRedis(ctx, []string{logicalChannel}, events)
	}()
	fullChannel := redisConfig(server.cfg).prefix + logicalChannel
	deadline := time.Now().Add(3 * time.Second)
	for {
		value, err := server.redisCommand(context.Background(), "PUBSUB", "NUMSUB", fullChannel)
		if err == nil && notificationIntegrationSubscriptionCount(value) > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("Redis subscription to %q was not ready: %v", fullChannel, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop := func() {
		cancel()
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("notification Redis subscription: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("notification Redis subscription did not stop")
		}
	}
	return events, stop
}

func notificationIntegrationSubscriptionCount(value any) int64 {
	items, ok := value.([]any)
	if !ok || len(items) != 2 {
		return 0
	}
	count, _ := items[1].(int64)
	return count
}

func waitForNotificationIntegrationMergedEvents(t *testing.T, events <-chan redisMessage, quietPeriod time.Duration) []redisMessage {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	quiet := time.NewTimer(quietPeriod)
	if !quiet.Stop() {
		<-quiet.C
	}
	merged := []redisMessage{}
	for {
		select {
		case event := <-events:
			if event.Event != "notifications_merged" {
				continue
			}
			merged = append(merged, event)
			quiet.Reset(quietPeriod)
		case <-quiet.C:
			return merged
		case <-deadline.C:
			if len(merged) == 0 {
				t.Fatal("timed out waiting for notifications_merged")
			}
			return merged
		}
	}
}

func deleteNotificationIntegrationRedisNamespace(server *Server) {
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, err := server.redisCommand(ctx, "KEYS", redisConfig(server.cfg).prefix+"*")
	if err != nil {
		return
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return
	}
	args := []string{"DEL"}
	for _, item := range items {
		if key, ok := item.(string); ok {
			args = append(args, key)
		}
	}
	if len(args) > 1 {
		_, _ = server.redisCommand(ctx, args...)
	}
}

func notificationIntegrationGroupKeys(groups []notificationGroupEntity) []string {
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.GroupKey)
	}
	return out
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalInts(left []int, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func notificationIntegrationContainsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
