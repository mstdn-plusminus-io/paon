package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
	"gorm.io/gorm"
)

const (
	railsPollAlreadyVotedMessage  = "Validation failed: You have already voted on this poll"
	railsPollExpiredMessage       = "Validation failed: The poll has already ended"
	railsPollInvalidChoiceMessage = "Validation failed: The chosen vote option does not exist"
	railsPollSelfVoteMessage      = "Validation failed: You cannot vote in your own polls"
	railsPollUpdateDelay          = 3 * time.Minute
)

func pollExpiredAt(expiresAt sql.NullTime, now time.Time) bool {
	return expiresAt.Valid && expiresAt.Time.Before(now.UTC())
}

func (s *Server) createPoll(c *echo.Context) error {
	return apiError(c, http.StatusNotFound, "Record not found")
}

func (s *Server) getPoll(c *echo.Context) error {
	c.Response().Header().Set("Vary", "Authorization")
	publicRESTCacheIfUnauthenticated(c, 15)
	if err := s.authorizeTokenScopeIfPresent(c, "read", "read:statuses"); err != nil {
		return err
	}
	account, _, _ := s.currentAccount(c)
	poll, err := s.findVisiblePollForAccount(account, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	if account != nil {
		if refreshed := s.refreshRemotePollIfStale(poll, account, time.Now().UTC()); refreshed {
			poll, err = s.findVisiblePollForAccount(account, c.Param("id"))
			if err != nil {
				return apiError(c, http.StatusNotFound, "Record not found")
			}
		}
	}
	if err := s.hydratePollCustomEmojis(poll); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.PollFromModel(s.cfg, poll, account))
}

func (s *Server) votePoll(c *echo.Context) error {
	account, _, err := s.requireAccountScope(c, "write", "write:statuses")
	if err != nil {
		return err
	}
	poll, err := s.findVisiblePollForAccount(account, c.Param("id"))
	if err != nil {
		return apiError(c, http.StatusNotFound, "Record not found")
	}
	choices, validChoices := pollVoteChoices(c)
	if !validChoices {
		return apiError(c, http.StatusUnprocessableEntity, railsPollInvalidChoiceMessage)
	}
	if len(choices) == 0 {
		if err := s.hydratePollCustomEmojis(poll); err != nil {
			return err
		}
		return c.JSON(http.StatusOK, serializer.PollFromModel(s.cfg, poll, account))
	}
	if pollExpiredAt(poll.ExpiresAt, time.Now().UTC()) {
		return apiError(c, http.StatusUnprocessableEntity, railsPollExpiredMessage)
	}
	if poll.AccountID.Valid && poll.AccountID.Int64 == account.ID {
		return apiError(c, http.StatusUnprocessableEntity, railsPollSelfVoteMessage)
	}
	if !poll.Multiple && len(choices) > 1 {
		return apiError(c, http.StatusUnprocessableEntity, railsPollAlreadyVotedMessage)
	}
	seen := map[int]struct{}{}
	for _, choice := range choices {
		if choice < 0 || choice >= len(poll.Options) {
			return apiError(c, http.StatusUnprocessableEntity, railsPollInvalidChoiceMessage)
		}
		if _, ok := seen[choice]; ok {
			return apiError(c, http.StatusUnprocessableEntity, railsPollAlreadyVotedMessage)
		}
		seen[choice] = struct{}{}
	}

	voteLockName := "vote:" + strconv.FormatInt(poll.ID, 10) + ":" + strconv.FormatInt(account.ID, 10)
	acquired, releaseVoteLock, err := s.acquireActivityPubRedisLock(c.Request().Context(), voteLockName, 15*time.Minute)
	if err != nil {
		return err
	}
	if !acquired {
		return apiError(c, http.StatusServiceUnavailable, "There was a temporary problem serving your request, please try again")
	}
	defer releaseVoteLock()

	var createdVotes []models.PollVote
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var existing []models.PollVote
		if err := tx.Where("poll_id = ? AND account_id = ?", poll.ID, account.ID).Find(&existing).Error; err != nil {
			return err
		}
		if !poll.Multiple && len(existing) > 0 {
			return errPollAlreadyVoted
		}
		existingChoices := map[int]struct{}{}
		for _, vote := range existing {
			existingChoices[vote.Choice] = struct{}{}
		}
		for _, choice := range choices {
			if _, ok := existingChoices[choice]; ok {
				return errPollAlreadyVoted
			}
		}

		now := time.Now().UTC()
		for _, choice := range choices {
			vote := models.PollVote{AccountID: models.PollVoteAccountID(account.ID), PollID: models.PollVotePollID(poll.ID), Choice: choice, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&vote).Error; err != nil {
				return err
			}
			vote.URI = models.NullSafeString(activityPubVoteURI(s, *account, vote.ID))
			if err := tx.Model(&models.PollVote{}).Where("id = ?", vote.ID).Update("uri", vote.URI).Error; err != nil {
				return err
			}
			createdVotes = append(createdVotes, vote)
			for len(poll.CachedTallies) <= choice {
				poll.CachedTallies = append(poll.CachedTallies, 0)
			}
			poll.CachedTallies[choice]++
			poll.VotesCount++
			poll.Votes = append(poll.Votes, vote)
		}
		updates := map[string]any{
			"cached_tallies": poll.CachedTallies,
			"votes_count":    poll.VotesCount,
			"updated_at":     now,
		}
		if len(existing) == 0 && poll.VotersCount.Valid {
			poll.VotersCount.Int64++
			updates["voters_count"] = poll.VotersCount.Int64
		}
		return tx.Model(&models.Poll{}).Where("id = ?", poll.ID).Updates(updates).Error
	})
	if err != nil {
		if err == errPollAlreadyVoted {
			return apiError(c, http.StatusUnprocessableEntity, railsPollAlreadyVotedMessage)
		}
		return err
	}
	if len(createdVotes) > 0 {
		s.activityTrackerIncrementBasic(c.Request().Context(), "activity:interactions", createdVotes[0].CreatedAt, 1)
	}
	if poll.StatusID.Valid {
		s.invalidateStatusCache(c.Request().Context(), poll.StatusID.Int64)
	}

	refreshed, err := s.findVisiblePollForAccount(account, c.Param("id"))
	if err != nil {
		return err
	}
	if refreshed.Account.Local() && !refreshed.HideTotals {
		s.enqueueActivityPubPollUpdateForPoll(*refreshed, railsPollUpdateDelay)
	}
	s.schedulePollExpirationFinalCheck(refreshed)
	_ = s.deliverActivityPubPollVotes(*account, *refreshed, createdVotes)
	if err := s.hydratePollCustomEmojis(refreshed); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, serializer.PollFromModel(s.cfg, refreshed, account))
}

func (s *Server) enqueueActivityPubPollUpdateForPoll(poll models.Poll, delay time.Duration) {
	if poll.StatusID.Valid && !s.enqueuePollUpdateTask(poll.StatusID.Int64, delay) {
		s.deliverActivityPubPollUpdateForPoll(poll)
	}
}

func (s *Server) deliverActivityPubPollUpdateForPoll(poll models.Poll) {
	if poll.StatusID.Valid {
		if status, err := s.findStatus(strconv.FormatInt(poll.StatusID.Int64, 10)); err == nil && status != nil {
			_ = s.deliverActivityPubPollUpdate(*status)
		}
	}
}

func (s *Server) refreshRemotePollIfStale(poll *models.Poll, signer *models.Account, now time.Time) bool {
	if !remotePollPossiblyStale(poll, now) || s.db == nil || !poll.StatusID.Valid {
		return false
	}
	var status models.Status
	if err := s.db.Where("id = ?", poll.StatusID.Int64).First(&status).Error; err != nil {
		return false
	}
	uri := status.URI.String
	if !status.URI.Valid || !activityPubHTTPURIAllowedRaw(uri) || s.localActivityURI(uri) {
		return false
	}
	payload, err := s.fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentAndSigner(uri, uri, paonUserAgent(s.cfg), signer)
	if err != nil {
		return false
	}
	note := payload.Object
	if note.Type != "" && !activityObjectIsStatus(note) {
		return false
	}
	actorURI := firstNonEmpty(payload.Actor, note.AttributedTo)
	if actorURI == "" || !activityAttributionTrusted(note.ID, actorURI) {
		return false
	}
	if note.AttributedTo == "" {
		note.AttributedTo = actorURI
	}
	actor, err := s.activityActorForURI(actorURI)
	if err != nil || actor == nil || !poll.AccountID.Valid || actor.ID != poll.AccountID.Int64 {
		return false
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		return s.syncActivityPubPoll(tx, &status, note, actor, now)
	})
	if err == nil {
		s.invalidateStatusCache(context.Background(), status.ID)
	}
	return err == nil
}

func remotePollPossiblyStale(poll *models.Poll, now time.Time) bool {
	if poll == nil || poll.Account.Local() {
		return false
	}
	lastFetchedBeforeExpiration := !poll.LastFetchedAt.Valid || !poll.ExpiresAt.Valid || poll.LastFetchedAt.Time.Before(poll.ExpiresAt.Time)
	timePassedSinceLastFetch := !poll.LastFetchedAt.Valid || poll.LastFetchedAt.Time.Before(now.Add(-1*time.Minute))
	return lastFetchedBeforeExpiration && timePassedSinceLastFetch
}

func (s *Server) findPoll(id string) (*models.Poll, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var poll models.Poll
	err := s.db.Preload("Votes").Preload("Account").Where("id = ? AND status_id IS NOT NULL", id).First(&poll).Error
	return &poll, err
}

func (s *Server) findVisiblePollForAccount(account *models.Account, id string) (*models.Poll, error) {
	if s.db == nil {
		return nil, gorm.ErrRecordNotFound
	}
	var poll models.Poll
	visibleStatusIDs := s.visibleStatusQuery(account).Select("statuses.id")
	err := s.db.Preload("Votes").
		Preload("Account").
		Where("polls.id = ? AND polls.status_id IS NOT NULL", id).
		Where("polls.status_id IN (?)", visibleStatusIDs).
		First(&poll).Error
	return &poll, err
}

func (s *Server) hydratePollCustomEmojis(poll *models.Poll) error {
	if poll == nil || s.db == nil {
		return nil
	}
	shortcodes := statusEmbedEmojiShortcodes(strings.Join(poll.Options, "\n"))
	if len(shortcodes) == 0 {
		poll.CustomEmojis = nil
		return nil
	}
	var emojis []models.CustomEmoji
	query := customEmojiDomainQuery(s.db.Where("shortcode IN ? AND disabled = false", shortcodes), poll.Account.Domain)
	if err := query.Find(&emojis).Error; err != nil {
		return err
	}
	poll.CustomEmojis = orderCustomEmojisByShortcode(shortcodes, emojis)
	return nil
}

func pollVoteChoices(c *echo.Context) ([]int, bool) {
	values := append([]string{}, c.QueryParams()["choices[]"]...)
	if len(values) == 0 && strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "application/json") {
		var body struct {
			Choices []json.RawMessage `json:"choices"`
		}
		if err := json.NewDecoder(c.Request().Body).Decode(&body); err == nil {
			for _, raw := range body.Choices {
				var number int
				if json.Unmarshal(raw, &number) == nil {
					values = append(values, strconv.Itoa(number))
					continue
				}
				var text string
				if json.Unmarshal(raw, &text) == nil {
					values = append(values, text)
				}
			}
		}
	}
	if len(values) == 0 {
		_ = c.Request().ParseForm()
		values = append(values, c.Request().PostForm["choices[]"]...)
	}
	out := []int{}
	for _, value := range values {
		choice, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, false
		}
		out = append(out, choice)
	}
	return out, true
}

type pollAlreadyVotedError struct{}

func (pollAlreadyVotedError) Error() string { return "already voted" }

var errPollAlreadyVoted error = pollAlreadyVotedError{}
