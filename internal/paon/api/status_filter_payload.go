package api

import (
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/mstdn-plusminus-io/paon/internal/paon/serializer"
)

func serializeStatusesWithFilterContext(cfg config.Config, statuses []models.Status, current *models.Account, filters []streamingFilter, filterContext string) []serializer.Status {
	out := make([]serializer.Status, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, statusWithFilterContext(cfg, status, current, filters, filterContext))
	}
	return out
}

func statusWithFilterContext(cfg config.Config, status models.Status, current *models.Account, filters []streamingFilter, filterContext string) serializer.Status {
	status = statusWithoutHashtagPreviewCards(status)
	item := serializer.StatusFromModel(cfg, status, current)
	if current == nil || len(filters) == 0 || strings.TrimSpace(filterContext) == "" {
		return item
	}
	return statusWithFilterResults(item, filters, "")
}

func statusWithAllFilterContexts(cfg config.Config, status models.Status, current *models.Account, filters []streamingFilter) serializer.Status {
	status = statusWithoutHashtagPreviewCards(status)
	item := serializer.StatusFromModel(cfg, status, current)
	if current == nil || len(filters) == 0 {
		return item
	}
	return statusWithFilterResults(item, filters, "")
}

func statusWithSourceAndFilterContext(cfg config.Config, status models.Status, current *models.Account, filters []streamingFilter, filterContext string) serializer.Status {
	status = statusWithoutHashtagPreviewCards(status)
	item := serializer.StatusFromModelWithSource(cfg, status, current)
	if current == nil || len(filters) == 0 || strings.TrimSpace(filterContext) == "" {
		return item
	}
	return statusWithFilterResults(item, filters, "")
}

func statusWithStreamingFilterContext(cfg config.Config, status models.Status, current *models.Account, filters []streamingFilter, filterContext string) serializer.Status {
	status = statusWithoutHashtagPreviewCards(status)
	item := serializer.StatusFromModel(cfg, status, current)
	if current == nil || len(filters) == 0 || strings.TrimSpace(filterContext) == "" {
		return item
	}
	return statusWithFilterResults(item, filters, filterContext)
}

func statusWithFilterResults(item serializer.Status, filters []streamingFilter, filterContext string) serializer.Status {
	if item.Reblog != nil {
		reblog := statusWithFilterResults(*item.Reblog, filters, filterContext)
		item.Reblog = &reblog
		item.Filtered = reblog.Filtered
	} else if payload, ok := notificationStatusPayloadMap(item); ok {
		results := streamingFilterResultsFromFilters(payload, filters, filterContext)
		if len(results) > 0 {
			item.Filtered = streamingFilterResultsAny(results)
		}
	}
	if quote, ok := item.Quote.(serializer.Quote); ok && quote.QuotedStatus != nil {
		quoted := statusWithFilterResults(*quote.QuotedStatus, filters, filterContext)
		quote.QuotedStatus = &quoted
		item.Quote = quote
	}
	return item
}

func statusListFilterContext(c *echo.Context) string {
	path := c.Request().URL.Path
	switch {
	case strings.Contains(path, "/api/v1/timelines/home"), strings.Contains(path, "/api/v1/timelines/list/"):
		return "home"
	case strings.Contains(path, "/api/v1/accounts/") && strings.HasSuffix(path, "/statuses"):
		return "account"
	case strings.Contains(path, "/api/v1/timelines/public"), strings.Contains(path, "/api/v1/timelines/tag/"), strings.Contains(path, "/api/v1/timelines/link"):
		return "public"
	case strings.Contains(path, "/api/v1/trends/statuses"), strings.Contains(path, "/api/v2/search"):
		return "public"
	case strings.Contains(path, "/api/v1/favourites"), strings.Contains(path, "/api/v1/bookmarks"):
		return "public"
	default:
		return ""
	}
}
