package api

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
)

func publicPathWithoutFormat(value string, format string) (string, bool) {
	suffix := "." + format
	if !strings.HasSuffix(strings.ToLower(value), suffix) {
		return value, false
	}
	return value[:len(value)-len(suffix)], true
}

func publicPathFormat(value string) (string, string, bool) {
	index := strings.LastIndex(value, ".")
	if index <= 0 || index == len(value)-1 {
		return value, "", false
	}
	return value[:index], strings.ToLower(value[index+1:]), true
}

func publicPathWithoutAnyFormat(value string) string {
	base, _, ok := publicPathFormat(value)
	if !ok {
		return value
	}
	return base
}

func publicRequestHasFormat(c *echo.Context, format string) bool {
	return strings.HasSuffix(strings.ToLower(c.Request().URL.Path), "."+format)
}

func publicRequestFormat(c *echo.Context) string {
	_, format, ok := publicPathFormat(c.Request().URL.Path)
	if !ok {
		return ""
	}
	return format
}

func requireHTMLOnlyOptionalFormat(c *echo.Context) error {
	format := strings.ToLower(strings.TrimSpace(c.Param("format")))
	if format == "" {
		format = publicRequestFormat(c)
	}
	switch format {
	case "", "html":
		return nil
	default:
		return noContentError(http.StatusNotAcceptable)
	}
}

func publicShortAccountParam(c *echo.Context, name string) string {
	if value := c.Param(name); value != "" {
		return value
	}
	value := c.Param(name + ".:format")
	if value == "" {
		return ""
	}
	if index := strings.LastIndex(value, "."); index > 0 {
		return value[:index]
	}
	return value
}

func withPathParam(c *echo.Context, name string, value string, fn func() error) error {
	values := c.PathValues()
	next := append(echo.PathValues(nil), values...)
	for i := range next {
		if next[i].Name != name {
			continue
		}
		original := next[i].Value
		next[i].Value = value
		c.SetPathValues(next)
		defer func() {
			next[i].Value = original
			c.SetPathValues(next)
		}()
		return fn()
	}
	c.SetPathValues(append(next, echo.PathValue{Name: name, Value: value}))
	defer c.SetPathValues(values)
	return fn()
}

func optionalFormatPathParam(name string, handler echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error {
		value := publicShortAccountParam(c, name)
		if value == "" {
			return handler(c)
		}
		return withPathParam(c, name, value, func() error {
			return handler(c)
		})
	}
}
