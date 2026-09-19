package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

func TestParseMastodon46CollectionPayload(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/collections", strings.NewReader(`{
		"name":"People","description":"Great accounts","language":"en",
		"sensitive":false,"discoverable":true,"tag_name":"Go","account_ids":[1,"2",1]
	}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	payload, err := parseCollectionPayload(echo.NewContext(req, httptest.NewRecorder(), echo.New()))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Name == nil || *payload.Name != "People" || payload.Sensitive == nil || *payload.Sensitive || payload.Discoverable == nil || !*payload.Discoverable {
		t.Fatalf("payload = %#v", payload)
	}
	if got := uniqueInt64s(payload.AccountIDs); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("account ids = %#v", payload.AccountIDs)
	}
}

func TestMastodon46CollectionRoutesIncludeStableAndAlphaAPIs(t *testing.T) {
	source, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, route := range []string{
		`e.GET("/api/v1/accounts/:id/collections", s.accountCollections)`,
		`e.GET("/api/v1/accounts/:id/in_collections", s.inCollections)`,
		`e.POST("/api/v1/collections", s.createCollection)`,
		`e.PATCH("/api/v1/collections/:id", s.updateCollection)`,
		`e.DELETE("/api/v1/collections/:collection_id/items/:id", s.deleteCollectionItem)`,
		`e.POST("/api/v1/collections/:collection_id/items/:id/revoke", s.revokeCollectionItem)`,
		`deprecatedAPIHandler("@1781049600", s.createCollection)`,
		`e.GET("/ap/users/:account_id/featured_collections", s.numericActivityPubAccountRoute(s.activityPubFeaturedCollections))`,
		`e.GET("/ap/users/:account_id/feature_authorizations/:id", s.numericActivityPubAccountRoute(s.activityPubFeatureAuthorization))`,
		`e.GET("/collections/:id", s.publicCollection)`,
	} {
		if !strings.Contains(text, route) {
			t.Fatalf("route missing %q", route)
		}
	}
}

func TestMastodon46FeaturePolicyDecision(t *testing.T) {
	viewer := &models.Account{ID: 9}
	local := models.Account{ID: 1, Discoverable: sql.NullBool{Bool: true, Valid: true}, Locked: true}
	if got := featurePolicyDecision(local, viewer, false, false); got != "denied" {
		t.Fatalf("locked local decision = %q", got)
	}
	if got := featurePolicyDecision(local, viewer, true, false); got != "automatic" {
		t.Fatalf("followed locked local decision = %q", got)
	}
	remote := models.Account{ID: 2, Domain: sql.NullString{String: "remote.example", Valid: true}, FeatureApprovalPolicy: featurePolicyPublic << 16}
	if got := featurePolicyDecision(remote, viewer, false, false); got != "automatic" {
		t.Fatalf("remote public decision = %q", got)
	}
}

func TestMastodon46FeaturePolicyParsesExplicitDisabledActor(t *testing.T) {
	account := models.Account{URI: "https://remote.example/users/alice"}
	if got := activityPubFeatureSubpolicyBitmap([]any{account.URI}, account); got != featurePolicyDisabled {
		t.Fatalf("disabled feature subpolicy = %d", got)
	}
}

func TestMastodon46ActivityContextIncludesFeaturedCollectionTerms(t *testing.T) {
	contexts := activityContext()
	extension, ok := contexts[len(contexts)-1].(map[string]any)
	if !ok {
		t.Fatalf("context = %#v", contexts)
	}
	for _, key := range []string{"FeaturedCollection", "FeaturedItem", "FeatureRequest", "FeatureAuthorization", "featuredObject", "featureAuthorization", "canFeature"} {
		if extension[key] == nil {
			t.Fatalf("activity context missing %q", key)
		}
	}
}

func TestMastodon46ActorContextIncludesFEP2c59AndInteractionTerms(t *testing.T) {
	context := activityPubActorContext()
	if len(context) < 4 || context[2] != "https://purl.archive.org/socialweb/webfinger" {
		t.Fatalf("actor context = %#v", context)
	}
	extension, ok := context[len(context)-1].(map[string]any)
	if !ok || extension["showMedia"] != "toot:showMedia" || extension["canFeature"] == nil {
		t.Fatalf("actor context extension = %#v", context[len(context)-1])
	}
}

func TestMastodon46RemoteActorAcceptsFEP2c59WebfingerWithoutPreferredUsername(t *testing.T) {
	body := []byte(`{"@context":["https://www.w3.org/ns/activitystreams","https://purl.archive.org/socialweb/webfinger"],"id":"https://actor.example/users/opaque","type":"Person","webfinger":"acct:alice@identity.example","inbox":"https://actor.example/users/opaque/inbox"}`)
	actor, err := parseRemoteActivityActor(body)
	if err != nil {
		t.Fatal(err)
	}
	if actor.Webfinger != "acct:alice@identity.example" {
		t.Fatalf("webfinger = %q", actor.Webfinger)
	}
	username, domain, ok := remoteActivityActorWebfingerIdentity(actor)
	if !ok || username != "alice" || domain != "identity.example" {
		t.Fatalf("identity = %q %q ok=%v", username, domain, ok)
	}
}

func TestMastodon465RemoteFeaturedCollectionCapsInboundItemsAt150(t *testing.T) {
	items := make([]any, 151)
	for i := range items {
		items[i] = map[string]any{"id": "https://remote.example/items/" + strconv.Itoa(i)}
	}
	got := remoteFeaturedCollectionItems(map[string]any{"orderedItems": items})
	if len(got) != 150 {
		t.Fatalf("capped items = %d", len(got))
	}
}

func TestMastodon46FetcherAcceptsFeaturedCollectionDocuments(t *testing.T) {
	body := []byte(`{"@context":"https://www.w3.org/ns/activitystreams","id":"https://remote.example/collections/1","type":"FeaturedCollection","attributedTo":"https://remote.example/users/alice","name":"Friends","sensitive":false,"discoverable":true,"orderedItems":[]}`)
	payload, err := parseActivityResourcePayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Type != "FeaturedCollection" || payload.Object.ID != "https://remote.example/collections/1" || payload.Object.AttributedTo != "https://remote.example/users/alice" {
		t.Fatalf("featured collection payload = %#v", payload)
	}
}

func TestMastodon46FetcherAcceptsFeatureAuthorizationDocuments(t *testing.T) {
	body := []byte(`{"@context":"https://www.w3.org/ns/activitystreams","id":"https://remote.example/authorizations/1","type":"FeatureAuthorization","interactingObject":"https://owner.example/collections/1","interactionTarget":"https://remote.example/users/alice"}`)
	payload, err := parseActivityResourcePayload(body)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Object.TypeExact != "FeatureAuthorization" || payload.Object.InteractingObject != "https://owner.example/collections/1" || payload.Object.InteractionTarget != "https://remote.example/users/alice" {
		t.Fatalf("feature authorization payload = %#v", payload.Object)
	}
}
