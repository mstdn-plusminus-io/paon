package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const asynqTaskDistributeAnnouncement = "admin:distribute_announcement_notification"

type asynqAnnouncementDistributionPayload struct {
	AnnouncementID int64 `json:"announcement_id"`
}

func (s *Server) adminAnnouncementPreviewPage(c *echo.Context) error {
	user, theme, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	announcement, err := s.findDistributableAnnouncement(c.Param("id"))
	if err != nil {
		return err
	}
	var userCount int64
	if err := s.announcementNotificationUsersQuery(s.db.WithContext(c.Request().Context())).Count(&userCount).Error; err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.HTML(http.StatusOK, adminAnnouncementPreviewHTML(announcement, userCount, user.Email, c.QueryParam("notice"), c.QueryParam("error"), locale, theme))
}

func (s *Server) testAdminAnnouncementDistribution(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	announcement, err := s.findDistributableAnnouncement(c.Param("id"))
	if err != nil {
		return err
	}
	if err := s.enqueueOrDeliverBulkMail(*user, "announcement", announcementPublishedMailMessage(s.cfg, *user, announcement)); err != nil {
		return err
	}
	locale := s.webLocale(c, user)
	return c.Redirect(http.StatusFound, "/admin/announcements/"+strconv.FormatInt(announcement.ID, 10)+"/preview?notice="+url.QueryEscape(adminT(locale, "admin.announcements.preview.sent", "Preview email sent")))
}

func (s *Server) distributeAdminAnnouncement(c *echo.Context) error {
	user, _, handled, err := s.requireAdminSettingsWebUser(c)
	if handled || err != nil {
		return err
	}
	announcement, err := s.findDistributableAnnouncement(c.Param("id"))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	result := s.db.WithContext(c.Request().Context()).Model(&models.Announcement{}).
		Where("id = ? AND published = ? AND notification_sent_at IS NULL", announcement.ID, true).
		Updates(map[string]any{"notification_sent_at": sql.NullTime{Time: now, Valid: true}, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return echo.NewHTTPError(http.StatusForbidden, "announcement notification has already been sent")
	}
	if err := s.enqueueAnnouncementDistributionTask(announcement.ID); err != nil {
		_ = s.db.WithContext(c.Request().Context()).Model(&models.Announcement{}).
			Where("id = ? AND notification_sent_at = ?", announcement.ID, now).
			Updates(map[string]any{"notification_sent_at": nil, "updated_at": time.Now().UTC()}).Error
		return err
	}
	return c.Redirect(http.StatusFound, "/admin/announcements?notice="+url.QueryEscape(adminT(s.webLocale(c, user), "admin.announcements.distribution_started", "User notification distribution started")))
}

func (s *Server) findDistributableAnnouncement(rawID string) (models.Announcement, error) {
	announcement, err := s.findAdminAnnouncement(rawID)
	if err != nil {
		return announcement, err
	}
	if !announcement.Published || announcement.NotificationSentAt.Valid {
		return announcement, echo.NewHTTPError(http.StatusForbidden, "announcement cannot be distributed")
	}
	return announcement, nil
}

func (s *Server) announcementNotificationUsersQuery(db *gorm.DB) *gorm.DB {
	return db.Model(&models.User{}).
		Joins("JOIN accounts ON accounts.id = users.account_id").
		Where("users.confirmed_at IS NOT NULL").
		Where("accounts.suspended_at IS NULL")
}

func (s *Server) enqueueAnnouncementDistributionTask(announcementID int64) error {
	if s == nil || s.asynqClient == nil {
		return fmt.Errorf("announcement distribution: asynq client is not configured")
	}
	if announcementID <= 0 {
		return fmt.Errorf("announcement distribution: announcement id is required")
	}
	payload, err := marshalAsynqTaskPayload(asynqAnnouncementDistributionPayload{AnnouncementID: announcementID})
	if err != nil {
		return fmt.Errorf("announcement distribution payload: %w", err)
	}
	task := asynq.NewTask(asynqTaskDistributeAnnouncement, payload,
		asynq.Queue(s.asynqQueue(asynqQueueDefault)), asynq.MaxRetry(10))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.asynqClient.EnqueueContext(ctx, task); !asynqEnqueueAccepted(err) {
		return fmt.Errorf("enqueue announcement distribution: %w", err)
	}
	return nil
}

func (s *Server) handleAsynqDistributeAnnouncement(ctx context.Context, task *asynq.Task) error {
	var payload asynqAnnouncementDistributionPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("announcement distribution: %w", err)
	}
	if s == nil || s.db == nil || payload.AnnouncementID <= 0 {
		return nil
	}
	var announcement models.Announcement
	if err := s.db.WithContext(ctx).Where("id = ? AND published = ?", payload.AnnouncementID, true).First(&announcement).Error; err != nil {
		return workerLookupError("announcement distribution lookup", err)
	}
	var users []models.User
	query := s.announcementNotificationUsersQuery(s.db.WithContext(ctx)).
		Select("users.id", "users.email", "users.unconfirmed_email", "users.locale", "users.settings", "users.disabled", "users.approved", "users.confirmed_at").
		Order("users.id ASC")
	if err := query.FindInBatches(&users, 500, func(_ *gorm.DB, _ int) error {
		for _, user := range users {
			if err := s.enqueueAnnouncementPublishedMail(user, announcement); err != nil {
				return fmt.Errorf("announcement mail for user %d: %w", user.ID, err)
			}
		}
		return nil
	}).Error; err != nil {
		return fmt.Errorf("announcement notification users: %w", err)
	}
	return nil
}

func (s *Server) enqueueAnnouncementPublishedMail(user models.User, announcement models.Announcement) error {
	message := announcementPublishedMailMessage(s.cfg, user, announcement)
	message.Bulk = true
	if s.asynqClient == nil {
		return sendMail(s.cfg, message)
	}
	payload, err := marshalAsynqTaskPayload(asynqMailerDeliveryPayload{UserID: user.ID, Eligibility: "bulk_announcement", Message: message})
	if err != nil {
		return fmt.Errorf("announcement mail payload: %w", err)
	}
	// A distribution task can retry after some mail tasks have already run.
	// Retain the deterministic task ID beyond the parent's retry window so that
	// those retries resume safely instead of delivering duplicate bulk mail.
	taskID := announcementDistributionMailTaskID(announcement.ID, user.ID)
	task := asynq.NewTask(asynqTaskMailerDelivery, payload,
		asynq.Queue(s.asynqQueue(asynqQueueMailers)), asynq.MaxRetry(25),
		asynq.TaskID(taskID), asynq.Retention(7*24*time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.asynqClient.EnqueueContext(ctx, task); !asynqEnqueueAccepted(err) {
		return fmt.Errorf("enqueue announcement mail: %w", err)
	}
	return nil
}

func announcementDistributionMailTaskID(announcementID int64, userID int64) string {
	return fmt.Sprintf("announcement-%d-user-%d", announcementID, userID)
}

func announcementPublishedMailMessage(cfg config.Config, user models.User, announcement models.Announcement) mailMessage {
	locale := mailLocale(cfg, user)
	vars := map[string]string{"domain": cfg.LocalDomain}
	subject := localizedMailText(locale, "user_mailer.announcement_published.subject", "Service announcement", nil)
	title := localizedMailText(locale, "user_mailer.announcement_published.title", "%{domain} service announcement", vars)
	description := localizedMailText(locale, "user_mailer.announcement_published.description", "The administrators of %{domain} are making an announcement:", vars)
	body := title + "\n\n===\n\n" + description + "\n\n" + strings.TrimSpace(announcement.Text) + "\n"
	return mailMessage{To: user.Email, Subject: subject, Body: body}
}

func adminAnnouncementPreviewHTML(announcement models.Announcement, userCount int64, email string, notice string, errorText string, localeAndTheme ...string) string {
	locale := settingsLocaleArgOrEnglish(localeAndTheme...)
	theme := settingsThemeArg(localeAndTheme...)
	id := strconv.FormatInt(announcement.ID, 10)
	body := `<div class="content__heading__actions"><a class="button" href="/admin/announcements">` + html.EscapeString(adminT(locale, "admin.announcements.back", "Back to announcements")) + `</a></div>` +
		`<div class="flash-message info">` + html.EscapeString(adminT(locale, "admin.announcements.preview.disclaimer", "Email appearance varies by mail client.")) + `</div>` +
		`<p class="lead">` + html.EscapeString(adminTVars(locale, "admin.announcements.preview.explanation", "This announcement can be emailed to %{count} users.", map[string]string{"count": strconv.FormatInt(userCount, 10)})) + `</p>` +
		`<div class="prose"><p>` + html.EscapeString(announcement.Text) + `</p></div><hr class="spacer">` +
		`<div class="content__heading__actions"><a class="button button-secondary" data-method="post" href="/admin/announcements/` + id + `/test">` + html.EscapeString(adminTVars(locale, "admin.terms_of_service.preview.send_preview", "Send preview to %{email}", map[string]string{"email": email})) + `</a> ` +
		`<a class="button" data-method="post" data-confirm="` + html.EscapeString(adminT(locale, "admin.reports.are_you_sure", "Are you sure?")) + `" href="/admin/announcements/` + id + `/distribution">` + html.EscapeString(adminTVars(locale, "admin.terms_of_service.preview.send_to_all", "Send to all %{count} users", map[string]string{"count": strconv.FormatInt(userCount, 10)})) + `</a></div>`
	return authPageHTML(adminT(locale, "admin.announcements.preview.title", "Preview announcement email"), notice, errorText, body, locale, theme)
}
