package api

import "strconv"

func streamingChannelIDsForSession(channel string, ids []string, session streamingSession) []string {
	if channel != "user" || session.Account == nil {
		return ids
	}
	accountID := strconv.FormatInt(session.Account.ID, 10)
	if tokenHasAnyScope(session.Scopes, "read", "read:notifications") {
		ids = appendStreamingChannelID(ids, "timeline:"+accountID+":notifications")
	}
	return ids
}

func appendStreamingChannelID(ids []string, channelID string) []string {
	for _, id := range ids {
		if id == channelID {
			return ids
		}
	}
	return append(ids, channelID)
}
