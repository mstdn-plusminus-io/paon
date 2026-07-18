package api

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
)

func TestAccountNoteCommentAcceptsJSON(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/accounts/1/note", strings.NewReader(`{"comment":"met at FediConf"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if got := accountNoteComment(c); got != "met at FediConf" {
		t.Fatalf("accountNoteComment = %q", got)
	}
}

func TestAccountNoteCommentPreservesNonBlankRailsCommentValue(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest("POST", "/api/v1/accounts/1/note", strings.NewReader("comment=+met+at+FediConf+"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, e)

	if got := accountNoteComment(c); got != " met at FediConf " {
		t.Fatalf("form accountNoteComment = %q", got)
	}

	req = httptest.NewRequest("POST", "/api/v1/accounts/1/note", strings.NewReader(`{"comment":" met at FediConf "}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	c = echo.NewContext(req, rec, e)

	if got := accountNoteComment(c); got != " met at FediConf " {
		t.Fatalf("json accountNoteComment = %q", got)
	}
}

func TestPinAccountUsesRailsAccountPinFollowValidation(t *testing.T) {
	src, err := os.ReadFile("account_notes_pins.go")
	if err != nil {
		t.Fatal(err)
	}
	if railsAccountPinFollowValidationMessage != "Validation failed: You must be already following the person you want to endorse" {
		t.Fatalf("pin validation message drifted: %q", railsAccountPinFollowValidationMessage)
	}
	if !functionBodyContains(t, src, "pinAccount", "railsAccountPinFollowValidationMessage") {
		t.Fatal("pinAccount must use the Rails AccountPin follow validation message")
	}
	if !functionBodyContains(t, src, "pinAccount", `errors.Is(err, gorm.ErrRecordNotFound)`) {
		t.Fatal("pinAccount must tolerate wrapped gorm.ErrRecordNotFound for the Rails AccountPin follow validation boundary")
	}
	if functionBodyContains(t, src, "pinAccount", `err == gorm.ErrRecordNotFound`) {
		t.Fatal("pinAccount must not directly compare gorm.ErrRecordNotFound")
	}
	for _, unexpected := range []string{
		"You cannot endorse yourself",
		"You must be following the account you want to endorse",
		"target.ID == account.ID",
	} {
		if strings.Contains(string(src), unexpected) {
			t.Fatalf("pinAccount must not keep Rails-incompatible validation fragment %q", unexpected)
		}
	}
}
