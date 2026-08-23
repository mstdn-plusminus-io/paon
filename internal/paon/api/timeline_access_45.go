package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	timelineAccessPublic        = "public"
	timelineAccessAuthenticated = "authenticated"
	timelineAccessDisabled      = "disabled"
)

type timelineAccessResult struct {
	account      *models.Account
	localOnly    bool
	remoteOnly   bool
	incompatible bool
}

// timelineAccessForRequest implements Mastodon 4.5's independent local and
// remote feed gates. topic selects the hashtag/link settings; otherwise the
// live/public timeline settings are used.
func (s *Server) timelineAccessForRequest(c *echo.Context, topic bool, local, remote bool) (timelineAccessResult, error) {
	localKey := "local_live_feed_access"
	remoteKey := "remote_live_feed_access"
	if topic {
		localKey = "local_topic_feed_access"
		remoteKey = "remote_topic_feed_access"
	}
	localMode := normalizeTimelineAccess(s.settingStringValue(localKey, timelineAccessPublic))
	remoteMode := normalizeTimelineAccess(s.settingStringValue(remoteKey, timelineAccessPublic))

	requireAuth := false
	switch {
	case local:
		requireAuth = localMode != timelineAccessPublic
	case remote:
		requireAuth = remoteMode != timelineAccessPublic
	default:
		requireAuth = localMode != timelineAccessPublic || remoteMode != timelineAccessPublic
	}

	var account *models.Account
	var err error
	if requireAuth {
		// Rails' require_user! distinguishes a missing user from an invalid
		// bearer token. Anonymous and application-only access to a gated feed
		// is a 422, while authorizeTokenScopeIfPresent keeps malformed tokens at
		// 401 and insufficient scopes at 403 before this point.
		if requestToken(c) == "" {
			return timelineAccessResult{}, apiError(c, http.StatusUnprocessableEntity, "This method requires an authenticated user")
		}
		account, _, err = s.requireAccount(c)
	} else {
		account, err = s.currentAccountForOptionalRequestToken(c)
	}
	if err != nil {
		return timelineAccessResult{}, err
	}

	var user *models.User
	if account != nil && account.User.ID != 0 {
		user = &account.User
	}
	localAllowed := s.timelineModeAllows(localMode, user)
	remoteAllowed := s.timelineModeAllows(remoteMode, user)
	localOnly := (local && !remote) || !remoteAllowed
	remoteOnly := (remote && !local) || !localAllowed

	return timelineAccessResult{
		account:      account,
		localOnly:    localOnly,
		remoteOnly:   remoteOnly,
		incompatible: localOnly && remoteOnly,
	}, nil
}

func normalizeTimelineAccess(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case timelineAccessAuthenticated:
		return timelineAccessAuthenticated
	case timelineAccessDisabled:
		return timelineAccessDisabled
	default:
		return timelineAccessPublic
	}
}

func normalizeLandingPage(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "about":
		return "about"
	case "local_feed":
		return "local_feed"
	default:
		return "trends"
	}
}

func (s *Server) timelineModeAllows(mode string, user *models.User) bool {
	switch normalizeTimelineAccess(mode) {
	case timelineAccessPublic:
		return true
	case timelineAccessAuthenticated:
		return user != nil && s.userCanUseAPI(*user)
	case timelineAccessDisabled:
		return user != nil && s.userCanUseAPI(*user) && s.userCan(user, rolePermissionViewFeeds)
	default:
		return false
	}
}
