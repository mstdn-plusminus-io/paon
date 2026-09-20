package api

import (
	"strings"

	"github.com/labstack/echo/v5"
)

func requestIsJSON(c *echo.Context) bool {
	return strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "application/json")
}

func limitParam(c *echo.Context, defaultValue int, maximum int) int {
	values, ok := c.Request().URL.Query()["limit"]
	if ok {
		raw := ""
		if len(values) > 0 {
			raw = values[0]
		}
		value := rubyToI(raw)
		if value < 0 {
			value = -value
		}
		if value > maximum {
			return maximum
		}
		return value
	}
	if defaultValue <= 0 || defaultValue > maximum {
		return maximum
	}
	return defaultValue
}
