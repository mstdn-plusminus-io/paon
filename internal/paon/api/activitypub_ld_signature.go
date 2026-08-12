package api

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"github.com/piprate/json-gold/ld"
	"gorm.io/gorm"
)

const (
	activityPubIdentityContext       = "https://w3id.org/identity/v1"
	activityPubSecurityContext       = "https://w3id.org/security/v1"
	activityPubJSONLDContextCacheTTL = 30 * 24 * time.Hour
	activityPubJSONLDContextMaxBytes = 1 << 20
)

func (s *Server) activityPubLinkedDataSignatureActor(body []byte, payload activityPayload) *models.Account {
	if !payload.Signature.Present || payload.Signature.Type != "RsaSignature2017" || payload.Signature.Creator == "" || payload.Signature.SignatureValue == "" {
		return nil
	}
	var document any
	if err := json.Unmarshal(body, &document); err != nil || activityPubHasUnsupportedSignedJSONLDFeature(document) {
		return nil
	}
	actor, err := s.activityPubLinkedDataSignatureCreatorActor(payload.Signature.Creator)
	if err != nil || actor == nil {
		return nil
	}
	publicKey, err := activityPublicKey(actor.PublicKey)
	if err != nil {
		if strings.TrimSpace(actor.PublicKey) != "" {
			return nil
		}
		actor, err = s.refreshActivityPubActorKey(payload.Signature.Creator, actor)
		if err != nil || actor == nil {
			return nil
		}
		publicKey, err = activityPublicKey(actor.PublicKey)
		if err != nil {
			return nil
		}
	}
	if !verifyActivityPubLinkedDataSignature(body, publicKey) {
		return nil
	}
	return actor
}

// Mastodon 4.3.23 deliberately does not grant linked-data-signature authority
// to documents using graph-restructuring keywords. JSON-LD canonicalization can
// otherwise drop or reorder the entry which the inbox later selects.
func activityPubHasUnsupportedSignedJSONLDFeature(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "@graph", "@included", "@reverse":
				return true
			}
			if activityPubHasUnsupportedSignedJSONLDFeature(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if activityPubHasUnsupportedSignedJSONLDFeature(child) {
				return true
			}
		}
	}
	return false
}

func (s *Server) activityPubLinkedDataSignatureCreatorActor(creator string) (*models.Account, error) {
	if s == nil || s.db == nil || strings.TrimSpace(creator) == "" {
		return nil, nil
	}
	if s.localActivityURI(creator) {
		return s.localAccountFromActivityURI(creator)
	}
	actorURI := creator
	if before, _, ok := strings.Cut(creator, "#"); ok {
		actorURI = before
	}
	var account models.Account
	err := s.db.Preload("AccountStat").Where("uri = ?", actorURI).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &account, err
}

func verifyActivityPubLinkedDataSignature(body []byte, publicKey *rsa.PublicKey) bool {
	if publicKey == nil {
		return false
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return false
	}
	signatureValue, toVerify, err := activityPubLinkedDataSignatureVerificationString(document)
	if err != nil {
		return false
	}
	signature, err := decodeActivityPubLinkedDataSignatureValue(signatureValue)
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(toVerify))
	return rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature) == nil
}

func (s *Server) signActivityPubLinkedDataSignaturePayload(signer models.Account, payload map[string]any) (map[string]any, error) {
	if payload == nil {
		return nil, fmt.Errorf("activitypub payload is nil")
	}
	if !signer.PrivateKey.Valid || strings.TrimSpace(signer.PrivateKey.String) == "" {
		return nil, fmt.Errorf("activitypub signer private key is missing")
	}
	key, err := activityPrivateKey(signer.PrivateKey.String)
	if err != nil {
		return nil, err
	}
	signed := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		signed[k] = v
	}
	signature := map[string]any{
		"type":    "RsaSignature2017",
		"creator": activityPubActorID(s, signer) + "#main-key",
		"created": time.Now().UTC().Format(time.RFC3339),
	}
	signed["signature"] = signature
	_, toSign, err := activityPubLinkedDataSignatureVerificationStringWithPlaceholder(signed)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(toSign))
	signatureValue, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return nil, err
	}
	signature["signatureValue"] = base64.StdEncoding.EncodeToString(signatureValue)
	signed["@context"] = activityPubContextWithSecurity(signed["@context"])
	return signed, nil
}

func (s *Server) signActivityPubLinkedDataSignaturePayloadIfNeeded(signer models.Account, status models.Status, payload map[string]any) (map[string]any, error) {
	if !s.activityPubPayloadShouldHaveLinkedDataSignature(status, payload) {
		return payload, nil
	}
	return s.signActivityPubLinkedDataSignaturePayload(signer, payload)
}

func (s *Server) signActivityPubLinkedDataSignaturePayloadWhenEnabled(signer models.Account, payload map[string]any) (map[string]any, error) {
	if s != nil && s.authorizedFetchMode() {
		return payload, nil
	}
	return s.signActivityPubLinkedDataSignaturePayload(signer, payload)
}

func (s *Server) activityPubPayloadShouldHaveLinkedDataSignature(status models.Status, payload map[string]any) bool {
	if payload == nil {
		return false
	}
	switch stringValue(payload["type"]) {
	case "Delete":
		return true
	case "Undo":
		object, _ := payload["object"].(map[string]any)
		return stringValue(object["type"]) == "Announce"
	default:
		if s != nil && s.authorizedFetchMode() {
			return false
		}
		return status.Visibility == 0 || status.Visibility == 1
	}
}

func activityPubContextWithSecurity(context any) any {
	values := make([]any, 0, 2)
	switch typed := context.(type) {
	case nil:
	case []any:
		values = append(values, typed...)
	case []string:
		for _, value := range typed {
			values = append(values, value)
		}
	default:
		values = append(values, typed)
	}
	values = uniqueActivityPubJSONLDContextValues(values)
	for _, value := range values {
		if stringValue(value) == activityPubSecurityContext {
			if len(values) == 1 {
				return values[0]
			}
			return values
		}
	}
	values = append(values, activityPubSecurityContext)
	if len(values) == 1 {
		return values[0]
	}
	return values
}

func uniqueActivityPubJSONLDContextValues(values []any) []any {
	seen := make(map[string]struct{}, len(values))
	out := make([]any, 0, len(values))
	for _, value := range values {
		key := activityPubJSONLDContextValueKey(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func activityPubJSONLDContextValueKey(value any) string {
	if str := stringValue(value); str != "" {
		return "s:" + str
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%T:%v", value, value)
	}
	return "j:" + string(encoded)
}

func activityPubLinkedDataSignatureVerificationStringWithPlaceholder(document map[string]any) (string, string, error) {
	signature, ok := activityPubLinkedDataSignatureMap(document)
	if !ok {
		return "", "", fmt.Errorf("missing compact signature")
	}
	if activityJSONLDString(signature, "signatureValue") == "" {
		originalSignature := signature
		signature = make(map[string]any, len(originalSignature)+1)
		for k, v := range originalSignature {
			signature[k] = v
		}
		signature["signatureValue"] = "placeholder"
		document = cloneActivityPubJSONLDDocument(document)
		document["signature"] = signature
	}
	return activityPubLinkedDataSignatureVerificationString(document)
}

func cloneActivityPubJSONLDDocument(document map[string]any) map[string]any {
	out := make(map[string]any, len(document))
	for k, v := range document {
		out[k] = v
	}
	return out
}

func activityPubLinkedDataSignatureVerificationString(document map[string]any) (string, string, error) {
	signature, ok := activityPubLinkedDataSignatureMap(document)
	if !ok {
		return "", "", fmt.Errorf("missing compact signature")
	}
	if signatureType := activityJSONLDType(signature); signatureType != "RsaSignature2017" {
		return "", "", fmt.Errorf("unsupported signature type %q", signatureType)
	}
	signatureValue := activityJSONLDString(signature, "signatureValue")
	if signatureValue == "" {
		return "", "", fmt.Errorf("missing signatureValue")
	}
	options := make(map[string]any, len(signature))
	for key, value := range signature {
		switch key {
		case "type", "@type", "id", "@id", "signatureValue":
			continue
		default:
			if activityPubJSONLDKeyMatchesTerm(key, "signatureValue") {
				continue
			}
			options[key] = value
		}
	}
	options["@context"] = activityPubIdentityContext

	unsigned := make(map[string]any, len(document)-1)
	for key, value := range document {
		if key != "signature" && !activityPubJSONLDKeyMatchesTerm(key, "signature") {
			unsigned[key] = value
		}
	}
	optionsHash, err := activityPubJSONLDHash(options)
	if err != nil {
		return "", "", err
	}
	documentHash, err := activityPubJSONLDHash(unsigned)
	if err != nil {
		return "", "", err
	}
	return signatureValue, optionsHash + documentHash, nil
}

func activityPubLinkedDataSignatureMap(document map[string]any) (map[string]any, bool) {
	signature, ok := activityJSONLDSingle(activityJSONLDValue(document, "signature")).(map[string]any)
	return signature, ok
}

func activityPubJSONLDKeyMatchesTerm(key string, term string) bool {
	if key == term {
		return true
	}
	for _, iri := range activityJSONLDTermIRIs(term) {
		if key == iri {
			return true
		}
	}
	return false
}

func activityPubJSONLDHash(value any) (string, error) {
	normalized, err := activityPubJSONLDNormalize(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(digest[:]), nil
}

func activityPubJSONLDNormalize(value any) (normalizedString string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			normalizedString = ""
			err = fmt.Errorf("JSON-LD normalization panic: %v", recovered)
		}
	}()
	value, err = activityPubJSONLDSanitize(value)
	if err != nil {
		return "", err
	}
	options := ld.NewJsonLdOptions("")
	options.Algorithm = ld.AlgorithmURDNA2015
	options.Format = "application/n-quads"
	options.DocumentLoader = activityPubJSONLDDocumentLoader()
	normalized, err := ld.NewJsonLdProcessor().Normalize(value, options)
	if err != nil {
		return "", err
	}
	normalizedString, ok := normalized.(string)
	if !ok {
		return "", fmt.Errorf("unexpected JSON-LD normalization result %T", normalized)
	}
	return normalizedString, nil
}

func activityPubJSONLDCompactToActivityStreams(value any) (compacted map[string]any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			compacted = nil
			err = fmt.Errorf("JSON-LD compaction panic: %v", recovered)
		}
	}()
	value, err = activityPubJSONLDSanitize(value)
	if err != nil {
		return nil, err
	}
	options := ld.NewJsonLdOptions("")
	options.DocumentLoader = activityPubJSONLDDocumentLoader()
	return ld.NewJsonLdProcessor().Compact(value, activityPubFullJSONLDContext(), options)
}

func activityPubFullJSONLDContext() []any {
	return []any{
		activityPubActivityStreamsContext(),
		activityPubSecurityContext,
		activityPubFullJSONLDContextExtensions(),
	}
}

func activityPubFullJSONLDContextExtensions() map[string]any {
	extensions := map[string]any{
		"manuallyApprovesFollowers": "as:manuallyApprovesFollowers",
		"sensitive":                 "as:sensitive",
		"Hashtag":                   "as:Hashtag",
		"movedTo":                   map[string]any{"@id": "as:movedTo", "@type": "@id"},
		"alsoKnownAs":               map[string]any{"@id": "as:alsoKnownAs", "@type": "@id"},
		"toot":                      "http://joinmastodon.org/ns#",
		"Emoji":                     "toot:Emoji",
		"featured":                  map[string]any{"@id": "toot:featured", "@type": "@id"},
		"featuredTags":              map[string]any{"@id": "toot:featuredTags", "@type": "@id"},
		"schema":                    "http://schema.org#",
		"PropertyValue":             "schema:PropertyValue",
		"value":                     "schema:value",
		"ostatus":                   "http://ostatus.org#",
		"atomUri":                   "ostatus:atomUri",
		"inReplyToAtomUri":          "ostatus:inReplyToAtomUri",
		"conversation":              "ostatus:conversation",
		"focalPoint":                map[string]any{"@container": "@list", "@id": "toot:focalPoint"},
		"blurhash":                  "toot:blurhash",
		"discoverable":              "toot:discoverable",
		"indexable":                 "toot:indexable",
		"memorial":                  "toot:memorial",
		"votersCount":               "toot:votersCount",
		"gts":                       "https://gotosocial.org/ns#",
		"interactionPolicy":         map[string]any{"@id": "gts:interactionPolicy", "@type": "@id"},
		"canQuote":                  map[string]any{"@id": "gts:canQuote", "@type": "@id"},
		"automaticApproval":         map[string]any{"@id": "gts:automaticApproval", "@type": "@id"},
		"manualApproval":            map[string]any{"@id": "gts:manualApproval", "@type": "@id"},
		"interactingObject":         map[string]any{"@id": "gts:interactingObject", "@type": "@id"},
		"interactionTarget":         map[string]any{"@id": "gts:interactionTarget", "@type": "@id"},
		"quoteUrl":                  "as:quoteUrl",
		"fep":                       "https://w3id.org/fep/044f#",
		"quote":                     map[string]any{"@id": "fep:quote", "@type": "@id"},
		"quoteAuthorization":        map[string]any{"@id": "fep:quoteAuthorization", "@type": "@id"},
		"QuoteAuthorization":        "fep:QuoteAuthorization",
		"QuoteRequest":              "fep:QuoteRequest",
		"misskey":                   "https://misskey-hub.net/ns#",
		"_misskey_quote":            "misskey:_misskey_quote",
	}
	return extensions
}

func activityPubJSONLDSanitize(value any) (any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode JSON-LD input: %w", err)
	}
	var sanitized any
	if err := json.Unmarshal(encoded, &sanitized); err != nil {
		return nil, fmt.Errorf("decode JSON-LD input: %w", err)
	}
	return sanitized, nil
}

func decodeActivityPubLinkedDataSignatureValue(value string) ([]byte, error) {
	filtered := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return r
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '+' || r == '/' || r == '=':
			return r
		default:
			return -1
		}
	}, value)
	if decoded, err := base64.StdEncoding.DecodeString(filtered); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(filtered, "="))
}

func activityPubJSONLDDocumentLoader() ld.DocumentLoader {
	loader := ld.NewCachingDocumentLoader(mastodonJSONLDDocumentLoader{client: activityPubJSONLDHTTPClient(), cache: true})
	activityStreams := map[string]any{"@context": activityPubActivityStreamsJSONLDContext()}
	security := map[string]any{"@context": activityPubSecurityJSONLDContext()}
	identity := map[string]any{"@context": activityPubIdentityJSONLDContext()}
	fep044f := map[string]any{"@context": activityPubFEP044FJSONLDContext()}
	toot := map[string]any{"@context": activityPubTootJSONLDContext()}
	gts := map[string]any{"@context": activityPubGTSJSONLDContext()}
	misskey := map[string]any{"@context": activityPubMisskeyJSONLDContext()}
	ostatus := map[string]any{"@context": activityPubOStatusJSONLDContext()}
	schema := map[string]any{"@context": activityPubSchemaJSONLDContext()}
	for _, uri := range []string{"https://www.w3.org/ns/activitystreams", "http://www.w3.org/ns/activitystreams", "https://www.w3.org/ns/activitystreams/", "http://www.w3.org/ns/activitystreams/", "https://www.w3.org/ns/activitystreams#", "http://www.w3.org/ns/activitystreams#"} {
		loader.AddDocument(uri, activityStreams)
	}
	for _, uri := range []string{activityPubSecurityContext, "http://w3id.org/security/v1", "https://w3id.org/security/v1/", "http://w3id.org/security/v1/"} {
		loader.AddDocument(uri, security)
	}
	for _, uri := range []string{activityPubIdentityContext, "http://w3id.org/identity/v1", "https://w3id.org/identity/v1/", "http://w3id.org/identity/v1/"} {
		loader.AddDocument(uri, identity)
	}
	for _, uri := range []string{"https://w3id.org/fep/044f", "https://w3id.org/fep/044f#"} {
		loader.AddDocument(uri, fep044f)
	}
	for _, uri := range []string{"http://joinmastodon.org/ns", "http://joinmastodon.org/ns#", "http://joinmastodon.org/ns/", "https://joinmastodon.org/ns", "https://joinmastodon.org/ns#", "https://joinmastodon.org/ns/"} {
		loader.AddDocument(uri, toot)
	}
	for _, uri := range []string{"https://gotosocial.org/ns", "https://gotosocial.org/ns#"} {
		loader.AddDocument(uri, gts)
	}
	for _, uri := range []string{"https://misskey-hub.net/ns", "https://misskey-hub.net/ns#"} {
		loader.AddDocument(uri, misskey)
	}
	for _, uri := range []string{"http://ostatus.org", "http://ostatus.org#", "http://ostatus.org/"} {
		loader.AddDocument(uri, ostatus)
	}
	for _, uri := range []string{"http://schema.org", "http://schema.org#", "http://schema.org/", "https://schema.org", "https://schema.org#", "https://schema.org/"} {
		loader.AddDocument(uri, schema)
	}
	return loader
}

type mastodonJSONLDDocumentLoader struct {
	client *http.Client
	cache  bool
}

func (loader mastodonJSONLDDocumentLoader) LoadDocument(uri string) (*ld.RemoteDocument, error) {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("unsupported JSON-LD context URI %q", uri)
	}
	if !activityFetchHostAllowed(parsed.Hostname()) {
		return nil, fmt.Errorf("remote host is not allowed")
	}
	if loader.cache {
		if doc, ok, err := activityPubJSONLDContextCacheGet(uri); ok || err != nil {
			return doc, err
		}
	}
	client := loader.client
	if client == nil {
		client = activityPubJSONLDHTTPClient()
	}
	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/ld+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JSON-LD context returned HTTP %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/ld+json" {
		return nil, fmt.Errorf("JSON-LD context returned unsupported content type %q", resp.Header.Get("Content-Type"))
	}
	if resp.ContentLength > activityPubJSONLDContextMaxBytes {
		return nil, fmt.Errorf("JSON-LD context exceeded %d bytes", activityPubJSONLDContextMaxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, activityPubJSONLDContextMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > activityPubJSONLDContextMaxBytes {
		return nil, fmt.Errorf("JSON-LD context exceeded %d bytes", activityPubJSONLDContextMaxBytes)
	}
	document, err := ld.DocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	documentURL := resp.Request.URL.String()
	if loader.cache {
		activityPubJSONLDContextCacheStore(uri, documentURL, body)
	}
	return &ld.RemoteDocument{DocumentURL: documentURL, Document: document}, nil
}

func activityPubJSONLDHTTPClient() *http.Client {
	client := *activityHTTPClientClone(10 * time.Second)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		if !activityRedirectAllowed(req, via) {
			return fmt.Errorf("remote host is not allowed")
		}
		return nil
	}
	return &client
}

type activityPubJSONLDContextCacheEntry struct {
	documentURL string
	body        []byte
	expiresAt   time.Time
}

var activityPubJSONLDContextCache = struct {
	sync.Mutex
	entries map[string]activityPubJSONLDContextCacheEntry
}{
	entries: make(map[string]activityPubJSONLDContextCacheEntry),
}

func activityPubJSONLDContextCacheGet(uri string) (*ld.RemoteDocument, bool, error) {
	now := time.Now()
	activityPubJSONLDContextCache.Lock()
	entry, ok := activityPubJSONLDContextCache.entries[uri]
	if ok && now.After(entry.expiresAt) {
		delete(activityPubJSONLDContextCache.entries, uri)
		ok = false
	}
	if !ok {
		activityPubJSONLDContextCache.Unlock()
		return nil, false, nil
	}
	body := append([]byte(nil), entry.body...)
	documentURL := entry.documentURL
	activityPubJSONLDContextCache.Unlock()

	document, err := ld.DocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, true, err
	}
	return &ld.RemoteDocument{DocumentURL: documentURL, Document: document}, true, nil
}

func activityPubJSONLDContextCacheStore(uri string, documentURL string, body []byte) {
	activityPubJSONLDContextCache.Lock()
	activityPubJSONLDContextCache.entries[uri] = activityPubJSONLDContextCacheEntry{
		documentURL: documentURL,
		body:        append([]byte(nil), body...),
		expiresAt:   time.Now().Add(activityPubJSONLDContextCacheTTL),
	}
	activityPubJSONLDContextCache.Unlock()
}

func activityPubFEP044FJSONLDContext() map[string]any {
	return map[string]any{
		"fep":                "https://w3id.org/fep/044f#",
		"quote":              map[string]any{"@id": "fep:quote", "@type": "@id"},
		"quoteAuthorization": map[string]any{"@id": "fep:quoteAuthorization", "@type": "@id"},
		"QuoteAuthorization": "fep:QuoteAuthorization",
		"QuoteRequest":       "fep:QuoteRequest",
	}
}

func activityPubTootJSONLDContext() map[string]any {
	return map[string]any{
		"toot":            "http://joinmastodon.org/ns#",
		"votersCount":     "toot:votersCount",
		"blurhash":        "toot:blurhash",
		"focalPoint":      map[string]any{"@id": "toot:focalPoint", "@container": "@list"},
		"featured":        map[string]any{"@id": "toot:featured", "@type": "@id"},
		"featuredTags":    map[string]any{"@id": "toot:featuredTags", "@type": "@id"},
		"discoverable":    "toot:discoverable",
		"indexable":       "toot:indexable",
		"memorial":        "toot:memorial",
		"suspended":       "toot:suspended",
		"fedibird":        "http://fedibird.com/ns#",
		"quoteUri":        "fedibird:quoteUri",
		"Emoji":           "toot:Emoji",
		"Digest":          "as:Digest",
		"digestAlgorithm": "as:digestAlgorithm",
		"digestValue":     "as:digestValue",
	}
}

func activityPubGTSJSONLDContext() map[string]any {
	return map[string]any{
		"gts":               "https://gotosocial.org/ns#",
		"interactionPolicy": map[string]any{"@id": "gts:interactionPolicy", "@type": "@id"},
		"canQuote":          map[string]any{"@id": "gts:canQuote", "@type": "@id"},
		"automaticApproval": map[string]any{"@id": "gts:automaticApproval", "@type": "@id"},
		"manualApproval":    map[string]any{"@id": "gts:manualApproval", "@type": "@id"},
		"interactingObject": map[string]any{"@id": "gts:interactingObject", "@type": "@id"},
		"interactionTarget": map[string]any{"@id": "gts:interactionTarget", "@type": "@id"},
	}
}

func activityPubMisskeyJSONLDContext() map[string]any {
	return map[string]any{
		"misskey":        "https://misskey-hub.net/ns#",
		"_misskey_quote": map[string]any{"@id": "misskey:_misskey_quote", "@type": "@id"},
	}
}

func activityPubOStatusJSONLDContext() map[string]any {
	return map[string]any{
		"ostatus":          "http://ostatus.org#",
		"atomUri":          "ostatus:atomUri",
		"inReplyToAtomUri": "ostatus:inReplyToAtomUri",
		"conversation":     "ostatus:conversation",
	}
}

func activityPubSchemaJSONLDContext() map[string]any {
	return map[string]any{
		"schema":        "http://schema.org#",
		"PropertyValue": "schema:PropertyValue",
		"value":         "schema:value",
	}
}

func activityPubActivityStreamsJSONLDContext() map[string]any {
	as := "https://www.w3.org/ns/activitystreams#"
	ctx := map[string]any{
		"@vocab":                    as,
		"as":                        as,
		"ostatus":                   "http://ostatus.org#",
		"schema":                    "http://schema.org#",
		"toot":                      "http://joinmastodon.org/ns#",
		"misskey":                   "https://misskey-hub.net/ns#",
		"fedibird":                  "http://fedibird.com/ns#",
		"gts":                       "https://gotosocial.org/ns#",
		"fep":                       "https://w3id.org/fep/044f#",
		"id":                        "@id",
		"type":                      "@type",
		"atomUri":                   "ostatus:atomUri",
		"inReplyToAtomUri":          "ostatus:inReplyToAtomUri",
		"content":                   as + "content",
		"contentMap":                map[string]any{"@id": as + "content", "@container": "@language"},
		"duration":                  map[string]any{"@id": as + "duration", "@type": "http://www.w3.org/2001/XMLSchema#duration"},
		"height":                    map[string]any{"@id": as + "height", "@type": "http://www.w3.org/2001/XMLSchema#nonNegativeInteger"},
		"mediaType":                 as + "mediaType",
		"name":                      as + "name",
		"nameMap":                   map[string]any{"@id": as + "name", "@container": "@language"},
		"published":                 map[string]any{"@id": as + "published", "@type": "http://www.w3.org/2001/XMLSchema#dateTime"},
		"rel":                       as + "rel",
		"source":                    as + "source",
		"startIndex":                map[string]any{"@id": as + "startIndex", "@type": "http://www.w3.org/2001/XMLSchema#nonNegativeInteger"},
		"summary":                   as + "summary",
		"summaryMap":                map[string]any{"@id": as + "summary", "@container": "@language"},
		"totalItems":                map[string]any{"@id": as + "totalItems", "@type": "http://www.w3.org/2001/XMLSchema#nonNegativeInteger"},
		"updated":                   map[string]any{"@id": as + "updated", "@type": "http://www.w3.org/2001/XMLSchema#dateTime"},
		"width":                     map[string]any{"@id": as + "width", "@type": "http://www.w3.org/2001/XMLSchema#nonNegativeInteger"},
		"manuallyApprovesFollowers": as + "manuallyApprovesFollowers",
		"sensitive":                 as + "sensitive",
		"votersCount":               "toot:votersCount",
		"blurhash":                  "toot:blurhash",
		"focalPoint":                map[string]any{"@id": "toot:focalPoint", "@container": "@list"},
		"featured":                  map[string]any{"@id": "toot:featured", "@type": "@id"},
		"featuredTags":              map[string]any{"@id": "toot:featuredTags", "@type": "@id"},
		"discoverable":              "toot:discoverable",
		"indexable":                 "toot:indexable",
		"memorial":                  "toot:memorial",
		"suspended":                 "toot:suspended",
		"quoteUrl":                  as + "quoteUrl",
		"quoteUri":                  "fedibird:quoteUri",
		"quote":                     map[string]any{"@id": "fep:quote", "@type": "@id"},
		"quoteAuthorization":        map[string]any{"@id": "fep:quoteAuthorization", "@type": "@id"},
		"QuoteAuthorization":        "fep:QuoteAuthorization",
		"QuoteRequest":              "fep:QuoteRequest",
		"_misskey_quote":            "misskey:_misskey_quote",
		"interactionPolicy":         "gts:interactionPolicy",
		"canQuote":                  map[string]any{"@id": "gts:canQuote", "@type": "@id"},
		"automaticApproval":         map[string]any{"@id": "gts:automaticApproval", "@type": "@id"},
		"manualApproval":            map[string]any{"@id": "gts:manualApproval", "@type": "@id"},
		"interactingObject":         map[string]any{"@id": "gts:interactingObject", "@type": "@id"},
		"interactionTarget":         map[string]any{"@id": "gts:interactionTarget", "@type": "@id"},
		"PropertyValue":             "schema:PropertyValue",
		"value":                     "schema:value",
		"Emoji":                     "toot:Emoji",
		"Digest":                    as + "Digest",
		"digestAlgorithm":           as + "digestAlgorithm",
		"digestValue":               as + "digestValue",
	}
	for _, term := range []string{
		"Accept", "Activity", "Add", "Announce", "Application", "Arrive", "Article", "Audio", "Block", "Collection", "CollectionPage", "Create", "Delete", "Dislike", "Document", "Event", "Flag", "Follow", "Group", "Ignore", "Image", "IntransitiveActivity", "Invite", "Join", "Leave", "Like", "Listen", "Mention", "Move", "Note", "Object", "Offer", "OrderedCollection", "OrderedCollectionPage", "Organization", "Page", "Person", "Place", "Profile", "Question", "Read", "Reject", "Relationship", "Remove", "Service", "TentativeAccept", "TentativeReject", "Tombstone", "Travel", "Undo", "Update", "Video", "View",
	} {
		ctx[term] = as + term
	}
	ctx["Hashtag"] = as + "Hashtag"
	for _, term := range []string{
		"accuracy", "altitude", "latitude", "longitude", "radius",
	} {
		ctx[term] = map[string]any{"@id": as + term, "@type": "http://www.w3.org/2001/XMLSchema#float"}
	}
	for _, term := range []string{
		"actor", "alsoKnownAs", "anyOf", "attachment", "attributedTo", "audience", "bcc", "bto", "cc", "context", "current", "describes", "endpoints", "first", "followers", "following", "formerType", "generator", "href", "icon", "image", "inReplyTo", "instrument", "items", "last", "liked", "likes", "location", "next", "object", "oneOf", "origin", "outbox", "partOf", "prev", "preview", "result", "replies", "sharedInbox", "shares", "subject", "tag", "target", "to", "url",
	} {
		ctx[term] = map[string]any{"@id": as + term, "@type": "@id"}
	}
	ctx["inbox"] = map[string]any{"@id": "http://www.w3.org/ns/ldp#inbox", "@type": "@id"}
	ctx["orderedItems"] = map[string]any{"@id": as + "items", "@type": "@id", "@container": "@list"}
	for _, term := range []string{"closed", "deleted", "endTime", "startTime"} {
		ctx[term] = map[string]any{"@id": as + term, "@type": "http://www.w3.org/2001/XMLSchema#dateTime"}
	}
	return ctx
}

func activityPubSecurityJSONLDContext() map[string]any {
	sec := "https://w3id.org/security#"
	ctx := map[string]any{
		"id":                        "@id",
		"type":                      "@type",
		"dc":                        "http://purl.org/dc/terms/",
		"sec":                       sec,
		"xsd":                       "http://www.w3.org/2001/XMLSchema#",
		"created":                   map[string]any{"@id": "http://purl.org/dc/terms/created", "@type": "http://www.w3.org/2001/XMLSchema#dateTime"},
		"creator":                   map[string]any{"@id": "http://purl.org/dc/terms/creator", "@type": "@id"},
		"expiration":                map[string]any{"@id": sec + "expiration", "@type": "http://www.w3.org/2001/XMLSchema#dateTime"},
		"expires":                   map[string]any{"@id": sec + "expiration", "@type": "http://www.w3.org/2001/XMLSchema#dateTime"},
		"owner":                     map[string]any{"@id": sec + "owner", "@type": "@id"},
		"privateKey":                map[string]any{"@id": sec + "privateKey", "@type": "@id"},
		"publicKey":                 map[string]any{"@id": sec + "publicKey", "@type": "@id"},
		"publicKeyService":          map[string]any{"@id": sec + "publicKeyService", "@type": "@id"},
		"revoked":                   map[string]any{"@id": sec + "revoked", "@type": "http://www.w3.org/2001/XMLSchema#dateTime"},
		"authenticationTag":         sec + "authenticationTag",
		"canonicalizationAlgorithm": sec + "canonicalizationAlgorithm",
		"cipherAlgorithm":           sec + "cipherAlgorithm",
		"cipherData":                sec + "cipherData",
		"cipherKey":                 sec + "cipherKey",
		"CryptographicKey":          sec + "Key",
		"EcdsaKoblitzSignature2016": sec + "EcdsaKoblitzSignature2016",
		"EncryptedMessage":          sec + "EncryptedMessage",
		"GraphSignature2012":        sec + "GraphSignature2012",
		"LinkedDataSignature2015":   sec + "LinkedDataSignature2015",
		"LinkedDataSignature2016":   sec + "LinkedDataSignature2016",
		"RsaSignature2017":          sec + "RsaSignature2017",
		"signature":                 sec + "signature",
		"signatureAlgorithm":        sec + "signingAlgorithm",
		"signatureValue":            sec + "signatureValue",
		"digestAlgorithm":           sec + "digestAlgorithm",
		"digestValue":               sec + "digestValue",
		"encryptionKey":             sec + "encryptionKey",
		"publicKeyPem":              sec + "publicKeyPem",
		"privateKeyPem":             sec + "privateKeyPem",
		"domain":                    sec + "domain",
		"initializationVector":      sec + "initializationVector",
		"iterationCount":            sec + "iterationCount",
		"nonce":                     sec + "nonce",
		"normalizationAlgorithm":    sec + "normalizationAlgorithm",
		"password":                  sec + "password",
		"salt":                      sec + "salt",
	}
	return ctx
}

func activityPubIdentityJSONLDContext() map[string]any {
	ctx := activityPubSecurityJSONLDContext()
	ctx["identity"] = "https://w3id.org/identity#"
	ctx["cred"] = "https://w3id.org/credentials#"
	ctx["idp"] = map[string]any{"@id": "https://w3id.org/identity#idp", "@type": "@id"}
	ctx["perm"] = "https://w3id.org/permissions#"
	ctx["ps"] = "https://w3id.org/payswarm#"
	ctx["rdf"] = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	ctx["rdfs"] = "http://www.w3.org/2000/01/rdf-schema#"
	ctx["schema"] = "http://schema.org/"
	ctx["Credential"] = "https://w3id.org/credentials#Credential"
	ctx["CryptographicKeyCredential"] = "https://w3id.org/credentials#CryptographicKeyCredential"
	ctx["Identity"] = "https://w3id.org/identity#Identity"
	ctx["comment"] = "http://www.w3.org/2000/01/rdf-schema#comment"
	ctx["label"] = "http://www.w3.org/2000/01/rdf-schema#label"
	ctx["Person"] = "http://schema.org/Person"
	ctx["Organization"] = "http://schema.org/Organization"
	ctx["Group"] = "https://www.w3.org/ns/activitystreams#Group"
	ctx["PostalAddress"] = "http://schema.org/PostalAddress"
	ctx["about"] = map[string]any{"@id": "http://schema.org/about", "@type": "@id"}
	ctx["accessControl"] = map[string]any{"@id": "https://w3id.org/permissions#accessControl", "@type": "@id"}
	ctx["address"] = map[string]any{"@id": "http://schema.org/address", "@type": "@id"}
	ctx["addressCountry"] = "http://schema.org/addressCountry"
	ctx["addressLocality"] = "http://schema.org/addressLocality"
	ctx["addressRegion"] = "http://schema.org/addressRegion"
	ctx["claim"] = map[string]any{"@id": "https://w3id.org/credentials#claim", "@type": "@id"}
	ctx["credential"] = map[string]any{"@id": "https://w3id.org/credentials#credential", "@type": "@id"}
	ctx["description"] = "http://schema.org/description"
	ctx["email"] = "http://schema.org/email"
	ctx["familyName"] = "http://schema.org/familyName"
	ctx["givenName"] = "http://schema.org/givenName"
	ctx["identityService"] = map[string]any{"@id": "https://w3id.org/identity#identityService", "@type": "@id"}
	ctx["image"] = map[string]any{"@id": "http://schema.org/image", "@type": "@id"}
	ctx["issued"] = map[string]any{"@id": "https://w3id.org/credentials#issued", "@type": "http://www.w3.org/2001/XMLSchema#dateTime"}
	ctx["issuer"] = map[string]any{"@id": "https://w3id.org/credentials#issuer", "@type": "@id"}
	ctx["member"] = map[string]any{"@id": "http://schema.org/member", "@type": "@id"}
	ctx["memberOf"] = map[string]any{"@id": "http://schema.org/memberOf", "@type": "@id"}
	ctx["name"] = "http://schema.org/name"
	ctx["paymentProcessor"] = "https://w3id.org/payswarm#processor"
	ctx["postalCode"] = "http://schema.org/postalCode"
	ctx["preferences"] = map[string]any{"@id": "https://w3id.org/payswarm#preferences", "@type": "@vocab"}
	ctx["recipient"] = map[string]any{"@id": "https://w3id.org/credentials#recipient", "@type": "@id"}
	ctx["streetAddress"] = "http://schema.org/streetAddress"
	ctx["title"] = "http://purl.org/dc/terms/title"
	ctx["url"] = map[string]any{"@id": "http://schema.org/url", "@type": "@id"}
	ctx["writePermission"] = map[string]any{"@id": "https://w3id.org/permissions#writePermission", "@type": "@id"}
	ctx["signatureAlgorithm"] = "https://w3id.org/security#signatureAlgorithm"
	return ctx
}
