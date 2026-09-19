package api

import "strings"

func activityActorImageDescription(value any) string {
	if values, ok := value.([]any); ok {
		if len(values) == 0 {
			return ""
		}
		return activityActorImageDescription(values[0])
	}
	image, ok := value.(map[string]any)
	if !ok || !activityValueHasCompactType(activityJSONLDValue(image, "type"), "Image") {
		return ""
	}
	description := strings.TrimSpace(firstNonEmpty(
		activityJSONLDString(image, "summary"),
		activityJSONLDString(image, "name"),
	))
	return truncateRunes(description, activityPubMediaAttachmentMaxDescriptionLength)
}
