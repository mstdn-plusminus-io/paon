package api

import (
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestCanonicalEmailBlockPayloadPrefersEmailOverHash(t *testing.T) {
	payload := adminCanonicalEmailBlockPayload{
		Email:              "example@email.com",
		CanonicalEmailHash: "dd501ce4e6b08698f19df96f2f15737e48a75660b1fa79b6ff58ea25ee4851a4",
	}
	if got, want := canonicalEmailBlockHashFromPayload(payload), canonicalEmailHash(payload.Email); got != want {
		t.Fatalf("canonical email block hash = %q, want email-derived %q", got, want)
	}
}

func TestOptionalCommentStringPreservesExplicitEmptyValue(t *testing.T) {
	if got := optionalCommentString("", false); got.Valid {
		t.Fatalf("missing comment = %#v, want null", got)
	}
	got := optionalCommentString("", true)
	if !got.Valid || got.String != "" {
		t.Fatalf("explicit empty comment = %#v, want valid empty string", got)
	}
}

func TestParseAdminDomainBlockPayloadAcceptsJSONBooleans(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/admin/domain_blocks", strings.NewReader(`{"domain":"remote.example","severity":"suspend","reject_media":true,"obfuscate":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminDomainBlockPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Domain != "remote.example" || payload.Severity != "suspend" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload.RejectMedia == nil || !*payload.RejectMedia || payload.Obfuscate == nil || !*payload.Obfuscate {
		t.Fatalf("boolean payload = %#v", payload)
	}
}

func TestParseAdminDomainBlockPayloadPreservesEmptyJSONComments(t *testing.T) {
	req := httptest.NewRequest("PATCH", "/api/v1/admin/domain_blocks/1", strings.NewReader(`{"private_comment":"","public_comment":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminDomainBlockPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.PrivateCommentSet || payload.PrivateComment != "" {
		t.Fatalf("private comment = %#v", payload)
	}
	if !payload.PublicCommentSet || payload.PublicComment != "" {
		t.Fatalf("public comment = %#v", payload)
	}
	if payload.DomainSet || payload.SeveritySet {
		t.Fatalf("unexpected flags = %#v", payload)
	}
}

func TestParseAdminDomainBlockPayloadPreservesEmptyFormComments(t *testing.T) {
	req := httptest.NewRequest("PATCH", "/api/v1/admin/domain_blocks/1", strings.NewReader("private_comment=&public_comment="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminDomainBlockPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.PrivateCommentSet || payload.PrivateComment != "" || !payload.PublicCommentSet || payload.PublicComment != "" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestParseAdminIPBlockPayloadPreservesEmptyJSONFields(t *testing.T) {
	req := httptest.NewRequest("PATCH", "/api/v1/admin/ip_blocks/1", strings.NewReader(`{"comment":"","expires_in":""}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminIPBlockPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.CommentSet || payload.Comment != "" {
		t.Fatalf("comment field = %#v", payload)
	}
	if !payload.ExpiresInSet || payload.ExpiresIn != "" {
		t.Fatalf("expires_in field = %#v", payload)
	}
	if payload.IPSet || payload.SeveritySet {
		t.Fatalf("unexpected field flags = %#v", payload)
	}
}

func TestParseAdminIPBlockPayloadPreservesEmptyFormFields(t *testing.T) {
	req := httptest.NewRequest("PATCH", "/api/v1/admin/ip_blocks/1", strings.NewReader("comment=&expires_in="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())

	payload, err := parseAdminIPBlockPayload(c)
	if err != nil {
		t.Fatal(err)
	}
	if !payload.CommentSet || payload.Comment != "" || !payload.ExpiresInSet || payload.ExpiresIn != "" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestAdminDomainBlocksRequireAdminRead(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/domain_blocks", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.adminDomainBlocks(c); err == nil {
		t.Fatal("expected admin domain blocks to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestAdminDomainControlWebAndImportHandlersKeepCacheSideEffects(t *testing.T) {
	tests := []struct {
		file string
		fn   string
		want string
	}{
		{"admin_domain_allows_web.go", "createAdminDomainAllowWeb", `s.materializeDomainControlMutation(c.Request().Context(), row.Domain)`},
		{"admin_domain_allows_web.go", "destroyAdminDomainAllowWeb", `s.refreshDomainControlMutation(c.Request().Context(), row.Domain)`},
		{"admin_domain_blocks_web.go", "createAdminDomainBlockWeb", `s.materializeDomainControlMutation(c.Request().Context(), block.Domain)`},
		{"admin_domain_blocks_web.go", "updateAdminDomainBlockWeb", `s.refreshDomainControlMutation(c.Request().Context(), block.Domain)`},
		{"admin_domain_blocks_web.go", "destroyAdminDomainBlockWeb", `s.refreshDomainControlMutation(c.Request().Context(), block.Domain)`},
		{"admin_domain_blocks_web.go", "batchAdminDomainBlocks", `s.materializeDomainControlMutation(c.Request().Context(), domain)`},
		{"admin_domain_exports.go", "importAdminDomainAllowsCSV", `s.materializeDomainControlMutation(c.Request().Context(), allow.Domain)`},
	}
	for _, tt := range tests {
		src, err := os.ReadFile(tt.file)
		if err != nil {
			t.Fatal(err)
		}
		if !functionBodyContains(t, src, tt.fn, tt.want) {
			t.Fatalf("%s in %s does not contain %q", tt.fn, tt.file, tt.want)
		}
	}
}

func TestAdminBlockListsUseRailsMinIDPagination(t *testing.T) {
	src, err := os.ReadFile("admin_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, rowName := range map[string]string{
		"adminDomainAllows":         "DomainAllow",
		"adminDomainBlocks":         "DomainBlock",
		"adminEmailDomainBlocks":    "EmailDomainBlock",
		"adminCanonicalEmailBlocks": "CanonicalEmailBlock",
		"adminIPBlocks":             "IPBlock",
	} {
		for _, want := range []string{
			`limitValue := limit(c, 100, 200)`,
			`if queryParamValuePresent(c, "min_id")`,
			`reverseRows(rows)`,
			`setLinkForRows(c, rows, func(row models.` + rowName + `) int64 { return row.ID }, limitValue)`,
		} {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("admin_blocks.go:%s does not contain %q", fn, want)
			}
		}
	}
	if !functionBodyContains(t, src, "setLinkForRows", `paginationLinkWithAllowedParams(c, id(rows[0]), id(rows[len(rows)-1]), "min_id", len(rows) == limitValue, true, adminLimitPaginationParams)`) {
		t.Fatal("setLinkForRows must emit bounded Rails-compatible pagination links")
	}
}

func TestAdminEmailDomainBlocksIncludeRailsHistory(t *testing.T) {
	src, err := os.ReadFile("admin_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"adminEmailDomainBlocks": {
			`out = append(out, s.adminEmailDomainBlockFromModel(c, row))`,
		},
		"showAdminEmailDomainBlock": {
			`return c.JSON(http.StatusOK, s.adminEmailDomainBlockFromModel(c, row))`,
		},
		"createAdminEmailDomainBlock": {
			`return c.JSON(http.StatusOK, s.adminEmailDomainBlockFromModel(c, row))`,
		},
		"adminEmailDomainBlockFromModel": {
			`serializer.AdminEmailDomainBlockFromModelWithHistory(block, s.emailDomainBlockHistory((*c).Request().Context(), block.ID, time.Now().UTC()))`,
		},
		"emailDomainBlockHistory": {
			`out := make([]serializer.AdminEmailDomainBlockHistory, 0, 7)`,
			`for i := 0; i < 7; i++ {`,
			`Day:      strconv.FormatInt(day.Unix(), 10)`,
		},
		"emailDomainBlockHistoryDay": {
			`usesKey, accountsKey := emailDomainBlockHistoryRedisKeys(s.cfg, blockID, day)`,
			`s.redisCommand(usesCtx, "GET", usesKey)`,
			`s.redisCommand(accountsCtx, "PFCOUNT", accountsKey)`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("admin_blocks.go:%s does not contain %q", fn, want)
			}
		}
	}
}

func TestCreateAdminEmailDomainBlockMapsDuplicateDomainsToRailsValidationError(t *testing.T) {
	src, err := os.ReadFile("admin_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if isUniqueConstraintError(err)`,
		`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Domain has already been taken")`,
	} {
		if !functionBodyContains(t, src, "createAdminEmailDomainBlock", want) {
			t.Fatalf("createAdminEmailDomainBlock does not contain %q", want)
		}
	}
}

func TestDeleteAdminEmailDomainBlockDeletesRailsDependentChildren(t *testing.T) {
	src, err := os.ReadFile("admin_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	if !functionBodyContains(t, src, "deleteAdminEmailDomainBlock", `Where("id = ? OR parent_id = ?", row.ID, row.ID).Delete(&models.EmailDomainBlock{})`) {
		t.Fatal("deleteAdminEmailDomainBlock does not delete child email domain blocks like Rails dependent destroy")
	}
}

func TestDomainRuleVariantsMatchRailsRuleForLookupOrder(t *testing.T) {
	got := domainRuleVariants(" Foo.Bar.Example.COM/ ")
	want := []string{"foo.bar.example.com", "bar.example.com", "example.com", "com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("domain variants = %#v, want %#v", got, want)
	}
}

func TestDomainBlockStricterThanMatchesRailsPolicyOrdering(t *testing.T) {
	silence := models.DomainBlock{Severity: domainBlockSeverityValue("silence")}
	suspend := models.DomainBlock{Severity: domainBlockSeverityValue("suspend")}
	noop := models.DomainBlock{Severity: domainBlockSeverityValue("noop")}
	mediaSilence := models.DomainBlock{Severity: domainBlockSeverityValue("silence"), RejectMedia: true}

	if !domainBlockStricterThan(suspend, silence) {
		t.Fatal("suspend should be stricter than silence")
	}
	if domainBlockStricterThan(silence, suspend) {
		t.Fatal("silence should not be stricter than suspend")
	}
	if domainBlockStricterThan(noop, silence) {
		t.Fatal("noop should not be stricter than silence")
	}
	if !domainBlockStricterThan(mediaSilence, silence) {
		t.Fatal("reject_media silence should be stricter than plain silence")
	}
}

func TestCreateAdminDomainBlockReturnsRailsExistingDomainBlockConflict(t *testing.T) {
	src, err := os.ReadFile("admin_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`existing, err := s.existingDomainBlockForDomain(domain)`,
		`if existing != nil && (existing.Domain == domain || !domainBlockStricterThan(row, *existing)) {`,
		`return c.JSON(http.StatusUnprocessableEntity, existingDomainBlockError(*existing, s.webLocale(c, user)))`,
		`errors.Is(err, gorm.ErrRecordNotFound)`,
	} {
		fn := "createAdminDomainBlock"
		if want == `errors.Is(err, gorm.ErrRecordNotFound)` {
			fn = "existingDomainBlockForDomain"
		}
		if !functionBodyContains(t, src, fn, want) {
			t.Fatalf("%s missing %q", fn, want)
		}
	}
	if functionBodyContains(t, src, "existingDomainBlockForDomain", `err == gorm.ErrRecordNotFound`) {
		t.Fatal("existingDomainBlockForDomain must not directly compare gorm.ErrRecordNotFound")
	}
}

func TestUpdateAdminIPBlockUsesExplicitFieldPresence(t *testing.T) {
	src, err := os.ReadFile("admin_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if payload.CommentSet {`,
		`updates["comment"] = payload.Comment`,
		`if payload.ExpiresInSet {`,
		`updates["expires_at"] = expiresAt(payload.ExpiresIn)`,
	} {
		if !functionBodyContains(t, src, "updateAdminIPBlock", want) {
			t.Fatalf("updateAdminIPBlock missing %q", want)
		}
	}
	if functionBodyContains(t, src, "updateAdminIPBlock", `if payload.Comment != ""`) || functionBodyContains(t, src, "updateAdminIPBlock", `if payload.ExpiresIn != ""`) {
		t.Fatal("updateAdminIPBlock must allow clearing comment and expires_at with empty strings")
	}
}

func TestAdminIPBlockHandlersValidateSeverityLikeRails(t *testing.T) {
	src, err := os.ReadFile("admin_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	for fn, checks := range map[string][]string{
		"createAdminIPBlock": {
			`severity, ok := ipBlockSeverityValue(payload.Severity)`,
			`if !ok {`,
			`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Severity can't be blank")`,
		},
		"updateAdminIPBlock": {
			`severity, ok := ipBlockSeverityValue(payload.Severity)`,
			`if !ok {`,
			`return apiError(c, http.StatusUnprocessableEntity, "Validation failed: Severity can't be blank")`,
		},
	} {
		for _, want := range checks {
			if !functionBodyContains(t, src, fn, want) {
				t.Fatalf("%s missing %q", fn, want)
			}
		}
	}
	if got := existingDomainBlockError(models.DomainBlock{Domain: "remote.example"}, "ja").Error; got != "あなたは既にremote.exampleさんに厳しい制限を課しています。" {
		t.Fatalf("localized existing domain block error = %q", got)
	}
}

func TestUpdateAdminDomainBlockUsesExplicitCommentPresence(t *testing.T) {
	src, err := os.ReadFile("admin_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`if payload.PrivateCommentSet {`,
		`updates["private_comment"] = payload.PrivateComment`,
		`if payload.PublicCommentSet {`,
		`updates["public_comment"] = payload.PublicComment`,
	} {
		if !functionBodyContains(t, src, "updateAdminDomainBlock", want) {
			t.Fatalf("updateAdminDomainBlock missing %q", want)
		}
	}
	if functionBodyContains(t, src, "updateAdminDomainBlock", `if payload.PrivateComment != ""`) || functionBodyContains(t, src, "updateAdminDomainBlock", `if payload.PublicComment != ""`) {
		t.Fatal("updateAdminDomainBlock must allow clearing comments with empty strings")
	}
}

func TestCreateAdminDomainBlockPreservesExplicitEmptyComments(t *testing.T) {
	src, err := os.ReadFile("admin_blocks.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`PrivateComment: optionalCommentString(payload.PrivateComment, payload.PrivateCommentSet)`,
		`PublicComment:  optionalCommentString(payload.PublicComment, payload.PublicCommentSet)`,
	} {
		if !functionBodyContains(t, src, "createAdminDomainBlock", want) {
			t.Fatalf("createAdminDomainBlock missing %q", want)
		}
	}
	if functionBodyContains(t, src, "createAdminDomainBlock", `PrivateComment: nullString(payload.PrivateComment)`) || functionBodyContains(t, src, "createAdminDomainBlock", `PublicComment:  nullString(payload.PublicComment)`) {
		t.Fatal("createAdminDomainBlock must not collapse explicit empty comments to null")
	}
}

func TestAdminIPBlocksRequireAdminRead(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/ip_blocks", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.adminIPBlocks(c); err == nil {
		t.Fatal("expected admin IP blocks to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func functionBodyContains(t *testing.T, src []byte, name string, want string) bool {
	t.Helper()
	return strings.Contains(functionBody(t, src, name), want)
}

func functionBody(t *testing.T, src []byte, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "admin_blocks.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			found = fn
			break
		}
	}
	if found == nil || found.Body == nil {
		t.Fatalf("function %s not found", name)
	}
	start := fset.Position(found.Body.Pos()).Offset
	end := fset.Position(found.Body.End()).Offset
	return string(src[start:end])
}

func TestIPBlockCacheRedisKeysMatchRailsCacheNamespaceCandidates(t *testing.T) {
	got := ipBlockCacheRedisKeys(config.Config{RedisNamespace: "mastodon:"})
	want := []string{"blocked_ips", "cache:blocked_ips", "mastodon:blocked_ips", "mastodon_cache:blocked_ips"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ip block cache keys = %#v, want %#v", got, want)
	}
}

func TestAdminCanonicalEmailBlocksRequireAdminRead(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/canonical_email_blocks", nil)
	rec := httptest.NewRecorder()
	c := echo.NewContext(req, rec, echo.New())
	s := &Server{}

	if err := s.adminCanonicalEmailBlocks(c); err == nil {
		t.Fatal("expected admin canonical email blocks to require authentication")
	} else if apiErr, ok := err.(apiHTTPError); !ok || apiErr.status != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestCanonicalEmailBlockForAccountMatchesRailsSuspendSideEffect(t *testing.T) {
	now := time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC)
	account := models.Account{
		ID:       42,
		Domain:   sql.NullString{},
		Username: "alice",
		User: models.User{
			ID:    7,
			Email: "Foo.Bar+baz@Example.COM",
		},
	}

	row, ok := canonicalEmailBlockForAccount(account, now)
	if !ok {
		t.Fatal("expected local account with email to produce a canonical email block")
	}
	if row.CanonicalEmailHash != "3bc3ca01dd1d501ca1c22e1c5d7d16feac90b8a3178fb17c710510d8a85e21bf" {
		t.Fatalf("canonical email hash = %q", row.CanonicalEmailHash)
	}
	if !row.ReferenceAccountID.Valid || row.ReferenceAccountID.Int64 != account.ID {
		t.Fatalf("reference account = %#v", row.ReferenceAccountID)
	}
	if !row.CreatedAt.Equal(now) || !row.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = %s/%s, want %s", row.CreatedAt, row.UpdatedAt, now)
	}
}

func TestCanonicalEmailBlockForAccountSkipsRemoteOrMissingEmail(t *testing.T) {
	now := time.Date(2026, 6, 20, 1, 2, 3, 0, time.UTC)
	tests := []models.Account{
		{ID: 1, Domain: sql.NullString{String: "remote.example", Valid: true}, User: models.User{ID: 2, Email: "a@example.com"}},
		{ID: 2, User: models.User{ID: 3}},
		{ID: 3, User: models.User{ID: 4, Email: "   "}},
		{ID: 4},
	}
	for _, account := range tests {
		if row, ok := canonicalEmailBlockForAccount(account, now); ok {
			t.Fatalf("account %#v unexpectedly produced %#v", account, row)
		}
	}
}

func TestAdminAccountSuspensionRoutesKeepCanonicalEmailBlockSideEffects(t *testing.T) {
	adminSrc, err := os.ReadFile("admin.go")
	if err != nil {
		t.Fatal(err)
	}
	webSrc, err := os.ReadFile("admin_accounts_web.go")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		file string
		src  []byte
		fn   string
		want string
	}{
		{"admin_accounts_web.go", webSrc, "stageReservedAdminAccountDeletion", "createCanonicalEmailBlockForAccountTx(tx, *account, now)"},
		{"admin.go", adminSrc, "adminAccountAction", "createCanonicalEmailBlockForAccountTx(tx, target, now)"},
		{"admin.go", adminSrc, "updateAdminAccount", "destroyCanonicalEmailBlocksForAccountTx(tx, account.ID)"},
		{"admin_accounts_web.go", webSrc, "applyAdminAccountWebAction", "destroyCanonicalEmailBlocksForAccountTx(tx, account.ID)"},
		{"admin_accounts_web.go", webSrc, "createAdminAccountWebWarning", "createCanonicalEmailBlockForAccountTx(tx, *account, now)"},
	}
	for _, tt := range tests {
		if !functionBodyContains(t, tt.src, tt.fn, tt.want) {
			t.Fatalf("%s:%s does not contain %q", tt.file, tt.fn, tt.want)
		}
	}
}
