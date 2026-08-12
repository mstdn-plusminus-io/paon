package api

import (
	"strings"

	"github.com/google/uuid"
)

func activityPubGeneratedPayloadURI(s *Server) string {
	return strings.TrimRight(s.cfg.BaseURL(), "/") + "/payloads/" + uuid.NewString()
}
