package api

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"math/big"
	"mime"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func mailAssertionText(t *testing.T, raw []byte) string {
	t.Helper()
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse mail: %v", err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("decode mail subject: %v", err)
	}
	return string(raw) + "\r\nSubject: " + subject + "\r\n"
}

func TestSMTPConfiguredRequiresServer(t *testing.T) {
	if smtpConfigured(config.Config{}) {
		t.Fatal("empty SMTP config was treated as configured")
	}
	if !smtpConfigured(config.Config{SMTPServer: "smtp.example.test"}) {
		t.Fatal("SMTP server was not treated as configured")
	}
}

func TestBuildMailMessageSanitizesHeadersAndBody(t *testing.T) {
	msg := mailAssertionText(t, buildMailMessage(config.Config{SMTPFrom: "noreply@example.test"}, mailMessage{
		To:      "alice@example.test\nBcc: bad@example.test",
		Subject: "Hello\r\nInjected: bad",
		Body:    ".first\nsecond",
		Headers: []mailHeader{
			{Key: "List-Unsubscribe", Value: "<https://example.test/unsubscribe>\r\nBad: yes"},
			{Key: "Bad:Header", Value: "dropped"},
		},
	}))
	for _, want := range []string{
		"From: noreply@example.test\r\n",
		"To: alice@example.test Bcc: bad@example.test\r\n",
		"Subject: Hello  Injected: bad\r\n",
		"List-Unsubscribe: <https://example.test/unsubscribe>  Bad: yes\r\n",
		"\r\n.first\r\nsecond\r\n",
		"Auto-Submitted: auto-generated\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("mail missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "Bad:Header") {
		t.Fatalf("invalid custom header was emitted:\n%s", msg)
	}
}

func TestSMTPAuthDomainPreservesRailsExplicitBlankDomain(t *testing.T) {
	if got := smtpDomainForAuth(config.Config{SMTPDomain: "", SMTPDomainSet: true, SMTPServer: "smtp.example.test"}); got != "" {
		t.Fatalf("explicit blank SMTP_DOMAIN auth domain = %q, want blank", got)
	}
	if got := smtpDomainForAuth(config.Config{SMTPDomain: "", SMTPServer: "smtp.example.test"}); got != "smtp.example.test" {
		t.Fatalf("unset SMTP_DOMAIN auth domain = %q, want server fallback", got)
	}
	if got := smtpDomainForAuth(config.Config{SMTPDomain: "mail.example.test", SMTPDomainSet: true, SMTPServer: "smtp.example.test"}); got != "mail.example.test" {
		t.Fatalf("explicit SMTP_DOMAIN auth domain = %q", got)
	}
}

func testSMTPCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "smtp.example.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestSecurityMailBodiesIncludeSettingsLinkAndCredentialName(t *testing.T) {
	s := &Server{cfg: config.Config{WebDomain: "example.test", Scheme: "https"}}
	user := models.User{
		Email:       "alice@example.test",
		Approved:    true,
		ConfirmedAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
	}
	msg := mailMessage{
		To:      user.Email,
		Subject: "Mastodon: New security key",
		Body:    "A new security key has been added\n\n=> " + s.cfg.BaseURL() + "/auth/edit\n\nUSB Key",
	}
	rendered := mailAssertionText(t, buildMailMessage(config.Config{SMTPFrom: "noreply@example.test"}, msg))
	for _, want := range []string{
		"Subject: Mastodon: New security key\r\n",
		"https://example.test/auth/edit",
		"USB Key",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("security mail missing %q:\n%s", want, rendered)
		}
	}
}

func TestUserReceivesSecurityMailRequiresActiveConfirmedUser(t *testing.T) {
	active := models.User{Email: "alice@example.test", Approved: true, ConfirmedAt: sql.NullTime{Time: time.Now().UTC(), Valid: true}}
	if !userReceivesSecurityMail(active) {
		t.Fatal("active confirmed user should receive security mail")
	}
	for _, user := range []models.User{
		{Email: "", Approved: true, ConfirmedAt: active.ConfirmedAt},
		{Email: "alice@example.test", Approved: false, ConfirmedAt: active.ConfirmedAt},
		{Email: "alice@example.test", Approved: true},
		{Email: "alice@example.test", Approved: true, ConfirmedAt: active.ConfirmedAt, Disabled: true},
	} {
		if userReceivesSecurityMail(user) {
			t.Fatalf("inactive user should not receive security mail: %#v", user)
		}
	}
}

func rubyMethodBody(t *testing.T, src []byte, name string) string {
	t.Helper()
	lines := strings.Split(string(src), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "def "+name || strings.HasPrefix(strings.TrimSpace(line), "def "+name+"(") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("Ruby method %s not found", name)
	}
	depth := 0
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "def ") || strings.HasSuffix(trimmed, " do") || strings.Contains(trimmed, " do |") {
			depth++
		}
		if trimmed == "end" {
			depth--
			if depth == 0 {
				return strings.Join(lines[start:i+1], "\n")
			}
		}
	}
	t.Fatalf("Ruby method %s end not found", name)
	return ""
}

func TestAccountWarningMailHelpersMatchRailsCopy(t *testing.T) {
	if got := accountWarningMailSubject("suspend", "@alice"); got != "Your account @alice has been suspended" {
		t.Fatalf("subject = %q", got)
	}
	if got := accountWarningMailTitle("silence"); got != "Account limited" {
		t.Fatalf("title = %q", got)
	}
	if got := accountWarningMailExplanation("delete_statuses", "example.test"); !strings.Contains(got, "moderators of example.test") {
		t.Fatalf("explanation = %q", got)
	}
	if got := accountWarningMailSubject("none", "@alice"); got != "Warning for @alice" {
		t.Fatalf("none subject = %q", got)
	}
}

func TestAccountWarningMailBodyCanRenderRailsRulesAndStatusExcerpts(t *testing.T) {
	cfg := config.Config{LocalDomain: "example.test", WebDomain: "example.test", Scheme: "https", DefaultLocale: "en"}
	user := models.User{
		Email: "alice@example.test",
		Account: &models.Account{
			ID:       3,
			Username: "alice",
		},
	}
	warning := models.AccountWarning{
		ID:        12,
		Action:    4000,
		Text:      "Repeated abuse",
		StatusIDs: models.StringArray{"100"},
		Report:    models.Report{ID: 4, Category: reportCategoryValue("violation"), RuleIDs: models.Int64Array{5}},
	}
	msg := accountWarningMailMessage(cfg, user, warning, accountWarningMailContext{
		Rules: []models.Rule{{ID: 5, Text: "No harassment"}},
		Statuses: []models.Status{{
			ID:          100,
			Text:        "<p>Hello &amp; welcome</p>",
			SpoilerText: "CW",
			Account:     models.Account{ID: 3, Username: "alice"},
		}},
	})
	rendered := mailAssertionText(t, buildMailMessage(config.Config{SMTPFrom: "noreply@example.test"}, msg))
	for _, want := range []string{
		"Subject: Your account @alice@example.test has been suspended\r\n",
		"- No harassment",
		"Posts cited:\r\n\r\n> CW\r\n> ----\r\n>",
		"> Hello & welcome",
		"View: https://example.test/@alice/100",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rich warning mail missing %q:\n%s", want, rendered)
		}
	}
}
