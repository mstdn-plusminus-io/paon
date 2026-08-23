package api

import (
	"context"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

var usernameHomoglyphNormalizer = strings.NewReplacer(
	"1", "i", "2", "z", "3", "e", "4", "a", "5", "s",
	"7", "t", "8", "b", "9", "g", "0", "o",
)

func normalizeBlockedUsername(value string) string {
	return usernameHomoglyphNormalizer.Replace(strings.ToLower(strings.TrimSpace(value)))
}

// usernameSignUpRestriction matches Mastodon 4.5's exact/contains rules after
// digit homoglyph normalization. Hard blocks reserve the username; approval
// blocks keep registration open but prevent automatic approval.
func (s *Server) usernameSignUpRestriction(ctx context.Context, username string) (blocked bool, requiresApproval bool, err error) {
	if s == nil || s.db == nil || strings.TrimSpace(username) == "" {
		return false, false, nil
	}
	normalized := normalizeBlockedUsername(username)
	var rows []models.UsernameBlock
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return false, false, err
	}
	for _, row := range rows {
		pattern := normalizeBlockedUsername(firstNonEmpty(row.NormalizedUsername, row.Username))
		if pattern == "" {
			continue
		}
		matches := normalized == pattern
		if !row.Exact {
			matches = strings.Contains(normalized, pattern)
		}
		if !matches {
			continue
		}
		if row.AllowWithApproval {
			requiresApproval = true
		} else {
			blocked = true
		}
	}
	return blocked, requiresApproval, nil
}

func (s *Server) usernameSignUpRequiresApproval(ctx context.Context, username string) (bool, error) {
	_, approval, err := s.usernameSignUpRestriction(ctx, username)
	return approval, err
}
