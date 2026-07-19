package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestConfirmationMailTaskUsesMailersQueueAndSafePayload(t *testing.T) {
	if asynqTaskConfirmationMail != "confirmation:mail" {
		t.Fatalf("confirmation mail task type = %q", asynqTaskConfirmationMail)
	}
	task, err := newAsynqConfirmationMailTask(42, " raw-token ", "mastodon:mailers")
	if err != nil {
		t.Fatal(err)
	}
	if task.Type() != asynqTaskConfirmationMail {
		t.Fatalf("task type = %q", task.Type())
	}
	var payload map[string]any
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload["user_id"] != float64(42) || payload["token"] != "raw-token" {
		t.Fatalf("confirmation mail payload = %#v", payload)
	}
	for _, sensitive := range []string{"email", "encrypted_password", "settings", "locale", "user"} {
		if _, ok := payload[sensitive]; ok {
			t.Fatalf("confirmation mail payload serializes %q: %#v", sensitive, payload)
		}
	}
	for _, invalid := range []struct {
		userID int64
		token  string
	}{
		{userID: 0, token: "token"},
		{userID: -1, token: "token"},
		{userID: 42, token: "  "},
	} {
		if _, err := newAsynqConfirmationMailTask(invalid.userID, invalid.token, "mailers"); err == nil {
			t.Fatalf("newAsynqConfirmationMailTask(%d, %q) succeeded", invalid.userID, invalid.token)
		}
	}

	src, err := os.ReadFile("asynq_workers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		fn   string
		want string
	}{
		{"newAsynqConfirmationMailTask", `asynq.Queue(queue)`},
		{"newAsynqConfirmationMailTask", `asynq.MaxRetry(25)`},
		{"enqueueConfirmationMailTask", `s.asynqQueue(asynqQueueMailers)`},
		{"enqueueConfirmationMailTask", `s.asynqClient.EnqueueContext(ctx, task)`},
		{"newAsynqServeMux", `mux.HandleFunc(asynqTaskConfirmationMail, s.handleAsynqConfirmationMail)`},
		{"loadConfirmationMailUser", `Select("id", "email", "unconfirmed_email", "confirmed_at", "approved", "locale", "settings")`},
	} {
		if !functionBodyContains(t, src, check.fn, check.want) {
			t.Fatalf("%s missing %q", check.fn, check.want)
		}
	}
}

func TestConfirmationDeliveryEnqueueSuccessFailureAndFallback(t *testing.T) {
	delivery := confirmationDelivery{
		Email:   "alice@example.test",
		Token:   "token",
		User:    models.User{ID: 42},
		HasUser: true,
	}

	enqueueCalls := 0
	deliverCalls := 0
	err := sendConfirmationDeliveryWithBackends(delivery, true, func(userID int64, token string) error {
		enqueueCalls++
		if userID != 42 || token != "token" {
			t.Fatalf("enqueue args = %d, %q", userID, token)
		}
		return nil
	}, func(confirmationDelivery) error {
		deliverCalls++
		return nil
	})
	if err != nil || enqueueCalls != 1 || deliverCalls != 0 {
		t.Fatalf("queued delivery: err=%v enqueue=%d deliver=%d", err, enqueueCalls, deliverCalls)
	}

	enqueueErr := errors.New("redis unavailable")
	enqueueCalls = 0
	deliverCalls = 0
	err = sendConfirmationDeliveryWithBackends(delivery, true, func(int64, string) error {
		enqueueCalls++
		return enqueueErr
	}, func(confirmationDelivery) error {
		deliverCalls++
		return nil
	})
	if !errors.Is(err, enqueueErr) || enqueueCalls != 1 || deliverCalls != 0 {
		t.Fatalf("failed enqueue: err=%v enqueue=%d deliver=%d", err, enqueueCalls, deliverCalls)
	}

	deliveryErr := errors.New("smtp unavailable")
	enqueueCalls = 0
	deliverCalls = 0
	err = sendConfirmationDeliveryWithBackends(delivery, false, func(int64, string) error {
		enqueueCalls++
		return nil
	}, func(got confirmationDelivery) error {
		deliverCalls++
		if got.Token != delivery.Token {
			t.Fatalf("fallback delivery = %#v", got)
		}
		return deliveryErr
	})
	if !errors.Is(err, deliveryErr) || enqueueCalls != 0 || deliverCalls != 1 {
		t.Fatalf("fallback delivery: err=%v enqueue=%d deliver=%d", err, enqueueCalls, deliverCalls)
	}

	delivery.User.ID = 0
	err = sendConfirmationDeliveryWithBackends(delivery, true, func(int64, string) error {
		t.Fatal("missing user id must not enqueue")
		return nil
	}, func(confirmationDelivery) error {
		t.Fatal("configured Asynq must not fall back to synchronous delivery")
		return nil
	})
	if err == nil {
		t.Fatal("queued delivery without a user id succeeded")
	}

	enqueueCalls = 0
	deliverCalls = 0
	delivery.Token = "  "
	err = sendConfirmationDeliveryWithBackends(delivery, true, func(int64, string) error {
		enqueueCalls++
		return nil
	}, func(confirmationDelivery) error {
		deliverCalls++
		return nil
	})
	if err != nil || enqueueCalls != 0 || deliverCalls != 0 {
		t.Fatalf("empty-token delivery: err=%v enqueue=%d deliver=%d", err, enqueueCalls, deliverCalls)
	}
}

func TestProcessAsynqConfirmationMailDeliversAndDiscardsMissingUser(t *testing.T) {
	now := time.Now().UTC()
	user := models.User{
		ID:                42,
		Email:             "old@example.test",
		UnconfirmedEmail:  sql.NullString{String: "new@example.test", Valid: true},
		ConfirmedAt:       sql.NullTime{Time: now, Valid: true},
		Approved:          false,
		Locale:            sql.NullString{String: "ja", Valid: true},
		EncryptedPassword: "must-not-be-serialized",
		ConfirmationToken: sql.NullString{String: "digest-only", Valid: true},
	}
	smtpErr := errors.New("smtp failed")
	loadCalls := 0
	deliverCalls := 0
	err := processAsynqConfirmationMail(context.Background(), asynqConfirmationMailPayload{UserID: 42, Token: "raw-token"}, func(_ context.Context, userID int64) (models.User, error) {
		loadCalls++
		if userID != user.ID {
			t.Fatalf("loaded user id = %d", userID)
		}
		return user, nil
	}, func(delivery confirmationDelivery) error {
		deliverCalls++
		if delivery.Email != "new@example.test" || delivery.Token != "raw-token" || !delivery.Reconfirmation || !delivery.HasUser {
			t.Fatalf("worker delivery = %#v", delivery)
		}
		if delivery.User.Locale.String != "ja" || delivery.User.Approved {
			t.Fatalf("worker did not preserve locale/approval rendering state: %#v", delivery.User)
		}
		return smtpErr
	})
	if !errors.Is(err, smtpErr) || loadCalls != 1 || deliverCalls != 1 {
		t.Fatalf("worker SMTP result: err=%v load=%d deliver=%d", err, loadCalls, deliverCalls)
	}

	deliverCalls = 0
	err = processAsynqConfirmationMail(context.Background(), asynqConfirmationMailPayload{UserID: 42, Token: "raw-token"}, func(context.Context, int64) (models.User, error) {
		return models.User{}, gorm.ErrRecordNotFound
	}, func(confirmationDelivery) error {
		deliverCalls++
		return nil
	})
	if err != nil || deliverCalls != 0 {
		t.Fatalf("missing user result: err=%v deliver=%d", err, deliverCalls)
	}

	task, err := newAsynqConfirmationMailTask(42, "raw-token", asynqQueueMailers)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Server{}).handleAsynqConfirmationMail(context.Background(), task); err != nil {
		t.Fatalf("handler should discard a user missing from its database: %v", err)
	}
}

func mustReadConfirmationQueueSource(t *testing.T, path string) []byte {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func assertConfirmationDeliveryOutsideClosure(t *testing.T, src []byte, name string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name+".go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if candidate, ok := decl.(*ast.FuncDecl); ok && candidate.Name.Name == name {
			fn = candidate
			break
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatalf("function %s not found", name)
	}
	funcLitDepth := 0
	stack := make([]bool, 0, 16)
	calls := 0
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		if node == nil {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if last {
				funcLitDepth--
			}
			return true
		}
		_, isFuncLit := node.(*ast.FuncLit)
		stack = append(stack, isFuncLit)
		if isFuncLit {
			funcLitDepth++
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "sendConfirmationDelivery" {
			return true
		}
		calls++
		if funcLitDepth != 0 {
			t.Fatalf("%s sends confirmation from inside a transaction/closure", name)
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("%s confirmation delivery calls = %d, want 1", name, calls)
	}
}
