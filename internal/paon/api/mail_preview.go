package api

import (
	"sync"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
)

const maxDevelopmentMailPreviews = 100

type developmentMailPreview struct {
	ID        int64
	To        string
	Subject   string
	Body      string
	Raw       string
	CreatedAt time.Time
}

var developmentMailPreviewStore = struct {
	sync.Mutex
	nextID int64
	items  []developmentMailPreview
}{}

func captureDevelopmentMailPreview(cfg config.Config, message mailMessage) bool {
	if !developmentMailPreviewEnabled() {
		return false
	}
	raw := string(buildMailMessage(cfg, message))
	developmentMailPreviewStore.Lock()
	defer developmentMailPreviewStore.Unlock()
	developmentMailPreviewStore.nextID++
	developmentMailPreviewStore.items = append([]developmentMailPreview{{
		ID:        developmentMailPreviewStore.nextID,
		To:        sanitizeMailAddress(message.To),
		Subject:   sanitizeHeader(message.Subject),
		Body:      normalizeMailBody(message.Body),
		Raw:       raw,
		CreatedAt: time.Now().UTC(),
	}}, developmentMailPreviewStore.items...)
	if len(developmentMailPreviewStore.items) > maxDevelopmentMailPreviews {
		developmentMailPreviewStore.items = developmentMailPreviewStore.items[:maxDevelopmentMailPreviews]
	}
	return true
}

func developmentMailPreviewEnabled() bool {
	return railsEnvNameFromProcess() != "production"
}

func developmentMailPreviews() []developmentMailPreview {
	developmentMailPreviewStore.Lock()
	defer developmentMailPreviewStore.Unlock()
	out := make([]developmentMailPreview, len(developmentMailPreviewStore.items))
	copy(out, developmentMailPreviewStore.items)
	return out
}

func developmentMailPreviewByID(id int64) (developmentMailPreview, bool) {
	developmentMailPreviewStore.Lock()
	defer developmentMailPreviewStore.Unlock()
	for _, item := range developmentMailPreviewStore.items {
		if item.ID == id {
			return item, true
		}
	}
	return developmentMailPreview{}, false
}

func resetDevelopmentMailPreviewsForTest() {
	developmentMailPreviewStore.Lock()
	defer developmentMailPreviewStore.Unlock()
	developmentMailPreviewStore.nextID = 0
	developmentMailPreviewStore.items = nil
}
