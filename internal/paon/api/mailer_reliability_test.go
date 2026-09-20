package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestBuildMailMessageProducesStandardsCompliantMultipartAlternative(t *testing.T) {
	raw := buildMailMessage(config.Config{
		SMTPFrom:       `"通知担当" <notify@example.test>`,
		SMTPReplyTo:    `"返信窓口" <reply@example.test>`,
		SMTPReturnPath: "bounce@example.test",
		LocalDomain:    "example.test",
	}, mailMessage{
		To:      `"山田 太郎" <taro@example.test>`,
		Subject: "セキュリティ通知",
		Body:    "本文です。\n\nhttps://example.test/auth/edit",
	})

	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	decoder := new(mime.WordDecoder)
	if subject, err := decoder.DecodeHeader(message.Header.Get("Subject")); err != nil || subject != "セキュリティ通知" {
		t.Fatalf("subject = %q, %v", subject, err)
	}
	for header, expected := range map[string]string{
		"From":     "notify@example.test",
		"To":       "taro@example.test",
		"Reply-To": "reply@example.test",
	} {
		address, err := mail.ParseAddress(message.Header.Get(header))
		if err != nil || address.Address != expected {
			t.Fatalf("%s = %#v, %v", header, address, err)
		}
	}
	if !strings.HasSuffix(message.Header.Get("Message-ID"), "@example.test>") {
		t.Fatalf("Message-ID = %q", message.Header.Get("Message-ID"))
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q %#v, %v", mediaType, params, err)
	}
	reader := multipart.NewReader(message.Body, params["boundary"])
	wantTypes := []string{"text/plain", "text/html"}
	for _, wantType := range wantTypes {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatalf("next %s part: %v", wantType, err)
		}
		partType, partParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil || partType != wantType || !strings.EqualFold(partParams["charset"], "UTF-8") {
			t.Fatalf("part Content-Type = %q %#v, %v", partType, partParams, err)
		}
		body, err := io.ReadAll(part)
		if err != nil || !strings.Contains(string(body), "本文です") {
			t.Fatalf("part body = %q, %v", body, err)
		}
	}
	if _, err := reader.NextPart(); err != io.EOF {
		t.Fatalf("unexpected additional MIME part: %v", err)
	}
}

func TestMailHTMLUsesMastodon43CardWithSafeActionsAndUnsubscribe(t *testing.T) {
	htmlBody := mailHTMLFromText(config.Config{Scheme: "https", LocalDomain: "social.example"}, "Security <notice>", "Hello <Alice>\n\n=> https://social.example/auth/edit", []mailHeader{
		{Key: "List-Unsubscribe", Value: "<https://social.example/unsubscribe?token=safe>"},
	})
	for _, want := range []string{
		`background:#f3f2f5`,
		`background:#1b001f`,
		`/packs/media/images/mailer-new/common/header-bg-start.png`,
		`Security &lt;notice&gt;`,
		`Hello &lt;Alice&gt;`,
		`href="https://social.example/auth/edit"`,
		`href="https://social.example/unsubscribe?token=safe"`,
	} {
		if !strings.Contains(htmlBody, want) {
			t.Fatalf("mail HTML missing %q: %s", want, htmlBody)
		}
	}
	if strings.Contains(htmlBody, "<Alice>") || strings.Contains(strings.ToLower(htmlBody), "javascript:") {
		t.Fatalf("mail HTML contains unsafe content: %s", htmlBody)
	}
	if _, ok := mailActionURL("=> javascript:alert(1)"); ok {
		t.Fatal("javascript mail action URL was accepted")
	}
}

func TestBuildMailMessageKeepsRailsAdminMailTextOnly(t *testing.T) {
	raw := buildMailMessage(config.Config{SMTPFrom: "notify@example.test"}, mailMessage{
		To:       "admin@example.test",
		Subject:  "Report",
		Body:     "Review the report",
		TextOnly: true,
	})
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/plain" || !strings.EqualFold(params["charset"], "UTF-8") {
		t.Fatalf("Content-Type = %q %#v, %v", mediaType, params, err)
	}
}

func TestNotificationRecipientMatchesRailsFunctionalUserContract(t *testing.T) {
	now := time.Now().UTC()
	functional := models.User{
		ID:          1,
		Email:       "alice@example.test",
		Approved:    true,
		ConfirmedAt: sql.NullTime{Time: now, Valid: true},
		Account:     &models.Account{ID: 10, Username: "alice"},
	}
	tests := []struct {
		name string
		user models.User
		want bool
	}{
		{name: "functional", user: functional, want: true},
		{name: "disabled", user: func() models.User { u := functional; u.Disabled = true; return u }()},
		{name: "unconfirmed", user: func() models.User { u := functional; u.ConfirmedAt = sql.NullTime{}; return u }()},
		{name: "unapproved", user: func() models.User { u := functional; u.Approved = false; return u }()},
		{name: "missing account", user: func() models.User { u := functional; u.Account = nil; return u }()},
		{name: "suspended", user: func() models.User {
			u := functional
			a := *u.Account
			a.SuspendedAt = sql.NullTime{Time: now, Valid: true}
			u.Account = &a
			return u
		}()},
		{name: "memorial", user: func() models.User { u := functional; a := *u.Account; a.Memorial = true; u.Account = &a; return u }()},
		{name: "moved", user: func() models.User {
			u := functional
			a := *u.Account
			a.MovedToAccountID = sql.NullInt64{Int64: 11, Valid: true}
			u.Account = &a
			return u
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := userReceivesNotificationMail(test.user); got != test.want {
				t.Fatalf("userReceivesNotificationMail() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestNotificationMailThreadsStatusByConversation(t *testing.T) {
	createdAt := time.Date(2026, time.July, 8, 12, 0, 0, 0, time.UTC)
	user := models.User{Email: "alice@example.test", Account: &models.Account{ID: 1, Username: "alice"}}
	notification := models.Notification{Type: "mention", FromAccount: models.Account{ID: 2, Username: "bob"}}
	status := &models.Status{ID: 3, Account: models.Account{ID: 2, Username: "bob"}, ConversationID: sql.NullInt64{Int64: 44, Valid: true}}
	message := notificationMailMessage(config.Config{LocalDomain: "social.example"}, user, notification, status, "https://social.example/@bob/3", models.Conversation{ID: 44, CreatedAt: createdAt})
	headers := map[string]string{}
	for _, header := range message.Headers {
		headers[header.Key] = header.Value
	}
	want := "<conversation-44.2026-07-08@social.example>"
	if headers["In-Reply-To"] != want || headers["References"] != want {
		t.Fatalf("thread headers = %#v", headers)
	}
}

func TestGenericMailerDeliveryTaskUsesMailersPayloadContract(t *testing.T) {
	task, err := newAsynqMailerDeliveryTask(42, "security", mailMessage{To: "alice@example.test", Subject: "Changed", Body: "body"}, "mastodon:mailers")
	if err != nil {
		t.Fatal(err)
	}
	if task.Type() != asynqTaskMailerDelivery {
		t.Fatalf("task type = %q", task.Type())
	}
	var payload asynqMailerDeliveryPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UserID != 42 || payload.Eligibility != "security" || payload.Message.To != "alice@example.test" {
		t.Fatalf("payload = %#v", payload)
	}
	for _, invalid := range []struct {
		userID      int64
		eligibility string
		message     mailMessage
	}{
		{userID: 0, eligibility: "security", message: mailMessage{To: "a@example.test", Subject: "x"}},
		{userID: 1, eligibility: "unknown", message: mailMessage{To: "a@example.test", Subject: "x"}},
		{userID: 1, eligibility: "security", message: mailMessage{}},
	} {
		if _, err := newAsynqMailerDeliveryTask(invalid.userID, invalid.eligibility, invalid.message, "mailers"); err == nil {
			t.Fatalf("invalid mail task succeeded: %#v", invalid)
		}
	}
}
