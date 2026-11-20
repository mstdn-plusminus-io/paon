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
	"reflect"
	"regexp"
	"slices"
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

func (s *Server) activityPubLinkedDataSignatureActor(body []byte, payload activityPayload) (*models.Account, error) {
	if !payload.Signature.Present {
		return nil, fmt.Errorf("linked-data signature is missing")
	}
	if payload.Signature.Type != "RsaSignature2017" {
		return nil, fmt.Errorf("unsupported linked-data signature type %q", payload.Signature.Type)
	}
	if payload.Signature.Creator == "" {
		return nil, fmt.Errorf("linked-data signature creator is missing")
	}
	if payload.Signature.SignatureValue == "" {
		return nil, fmt.Errorf("linked-data signature value is missing")
	}
	// Also rejects @graph, @included and @reverse like
	// LinkedDataSignature#verify_actor!'s unsupported_jsonld_features? check.
	if err := activityPubLinkedDataSignatureCoversDocument(body); err != nil {
		return nil, err
	}
	actor, err := s.activityPubLinkedDataSignatureCreatorActor(payload.Signature.Creator)
	if err != nil {
		return nil, fmt.Errorf("resolve linked-data signature creator %q: %w", payload.Signature.Creator, err)
	}
	if actor == nil {
		return nil, fmt.Errorf("resolve linked-data signature creator %q: actor is missing", payload.Signature.Creator)
	}
	publicKey, err := activityPublicKey(actor.PublicKey)
	if err != nil {
		if strings.TrimSpace(actor.PublicKey) != "" {
			return nil, fmt.Errorf("parse linked-data signature creator public key: %w", err)
		}
		actor, err = s.refreshActivityPubActorKey(payload.Signature.Creator, actor)
		if err != nil {
			return nil, fmt.Errorf("refresh linked-data signature creator public key: %w", err)
		}
		if actor == nil {
			return nil, fmt.Errorf("refresh linked-data signature creator public key: actor is missing")
		}
		publicKey, err = activityPublicKey(actor.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("parse refreshed linked-data signature creator public key: %w", err)
		}
	}
	if err := verifyActivityPubLinkedDataSignatureWithError(body, publicKey); err != nil {
		var diagnostic *activityPubSignatureVerificationError
		if errors.As(err, &diagnostic) {
			diagnostic.Diagnostics.SignatureActorID = actor.ID
			diagnostic.Diagnostics.SignatureActorURI = activityPubSafeLogValue(actor.URI, 512)
			diagnostic.Diagnostics.ActorUpdatedAt = actor.UpdatedAt.UTC().Format(time.RFC3339Nano)
		}
		return nil, err
	}
	return actor, nil
}

// Mastodon 4.3.23 deliberately does not grant linked-data-signature authority
// to documents using graph-restructuring keywords. JSON-LD canonicalization can
// otherwise drop or reorder the entry which the inbox later selects.
func activityPubHasUnsupportedSignedJSONLDFeature(value any) bool {
	_, ok := activityPubJSONLDUnsupportedKeyword(value)
	return ok
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
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		if err != nil {
			return nil, err
		}
		return &account, nil
	}
	// Mastodon's LinkedDataSignature resolves an unknown creator through
	// FetchRemoteKeyService before verifying the document. Reuse the guarded
	// HTTP-signature key resolver so relays can forward actors not yet known
	// to this server without weakening domain, WebFinger, key-owner, or SSRF
	// checks.
	return s.activityPubActorFromKeyID(creator)
}

func verifyActivityPubLinkedDataSignature(body []byte, publicKey *rsa.PublicKey) bool {
	return verifyActivityPubLinkedDataSignatureWithError(body, publicKey) == nil
}

func verifyActivityPubLinkedDataSignatureWithError(body []byte, publicKey *rsa.PublicKey) error {
	if publicKey == nil {
		return fmt.Errorf("linked-data signature public key is missing")
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("decode linked-data signature document: %w", err)
	}
	signatureValue, toVerify, err := activityPubLinkedDataSignatureVerificationString(document)
	if err != nil {
		return fmt.Errorf("prepare linked-data signature verification: %w", err)
	}
	signature, err := decodeActivityPubLinkedDataSignatureValue(signatureValue)
	if err != nil {
		return fmt.Errorf("decode linked-data signature value: %w", err)
	}
	digest := sha256.Sum256([]byte(toVerify))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		return newActivityPubSignatureVerificationError(fmt.Errorf("verify linked-data RSA signature: %w", err), body, document, publicKey, signature, toVerify)
	}
	return nil
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
	return activityPubLinkedDataSignatureVerificationStringWithHash(document, activityPubJSONLDHash)
}

func activityPubLinkedDataSignatureVerificationStringWithHash(document map[string]any, hash func(any) (string, error)) (string, string, error) {
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

	// Hash everything except the literal signature object, as Mastodon does
	// with `@json.without('signature')`. Alias keys are not signatures, so they
	// must stay covered by the signature instead of being silently excluded.
	unsigned := make(map[string]any, len(document)-1)
	for key, value := range document {
		if key != "signature" {
			unsigned[key] = value
		}
	}
	optionsHash, err := hash(options)
	if err != nil {
		return "", "", fmt.Errorf("normalize linked-data signature options: %w", err)
	}
	documentHash, err := hash(unsigned)
	if err != nil {
		return "", "", fmt.Errorf("normalize linked-data signature document: %w", err)
	}
	return signatureValue, optionsHash + documentHash, nil
}

// activityPubLinkedDataSignatureMap returns the document's linked-data
// signature. Like Mastodon (`@json['signature'].is_a?(Hash)` in
// LinkedDataSignature#verify_actor! and ProcessCollectionService), only a
// literal top-level "signature" JSON object counts, and it is never unwrapped
// (an {"@list": [...]} object has no signature type). Arrays and IRI/CURIE
// aliases such as "sec:signature" are not signatures: such documents are never
// JSON-LD compacted, so verifying them would canonicalize the sender's own
// @context while Paon reads their literal keys.
func activityPubLinkedDataSignatureMap(document map[string]any) (map[string]any, bool) {
	signature, ok := document["signature"].(map[string]any)
	return signature, ok && signature != nil
}

// activityPubJSONLDLanguageTag mirrors json-gold's validLanguageRegex
// (ld/rdf_dataset.go): a literal with any other language tag is left out of
// the canonicalized dataset.
var activityPubJSONLDLanguageTag = regexp.MustCompile(`^[a-zA-Z]+(-[a-zA-Z0-9]+)*$`)

// activityPubLinkedDataSignatureCoversDocument rejects a compacted document
// that holds content outside the RDF dataset its linked-data signature covers.
// json-gold leaves out every node and reference whose @id is a relative IRI
// (GraphToRDF in ld/rdf_dataset.go, objectToRDF in ld/node.go) and every
// statement with a malformed http(s) IRI, language tag or datatype
// (Quad.Valid), so such content keeps a valid signature while Paon still reads
// it, e.g. a publicKey with a relative @id placed before the signed key.
// Properties that expand to blank node identifiers (terms no context defines)
// are never canonicalized either; as in Mastodon they are left alone. That is
// only safe while compaction keeps them under their "_:" keys; see
// activityPubFullJSONLDContextExtensions.
func activityPubLinkedDataSignatureCoversDocument(body []byte) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("JSON-LD expansion panic: %v", recovered)
		}
	}()
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return fmt.Errorf("decode linked-data signature document: %w", err)
	}
	// Mastodon 0786c1e57a (JsonLdHelper#unsupported_jsonld_features?) does not
	// authenticate a compacted document that holds @graph, @included or
	// @reverse anywhere: they let the JSON tree a server reads (Paon unwraps a
	// nested @graph into the object) diverge from the signed default graph.
	if keyword, ok := activityPubJSONLDUnsupportedKeyword(document); ok {
		return fmt.Errorf("linked-data signature does not cover the document: %s is not supported", keyword)
	}
	unsigned := make(map[string]any, len(document))
	for key, value := range document {
		if key == "signature" {
			continue
		}
		if key == "@context" {
			unsigned[key] = value
			continue
		}
		// Compaction only writes Paon's own context at the top level and the
		// keywords listed in activityPubJSONLDCompactedKeywords below it. Any
		// other keyword comes from a blank node property such as "_:@context",
		// which json-gold compacts against the ActivityStreams "@vocab": "_:"
		// to that keyword. Re-expanding the document would then act on it. An
		// embedded "@context" could null or retype the terms Paon reads: the
		// values under those terms would leave the canonicalized RDF, and
		// nulled ones would not even appear in the expanded document checked
		// below. "@nest" and "@set" would move the signed values under them
		// back into the enclosing node, so the signature would still cover
		// them while Paon no longer saw them under the keys it reads, and
		// "@id" would give a node a blank node identifier. Such a document is
		// never authenticated, nor is one with a map that holds "@language"
		// or "@direction" but no "@value" (activityPubJSONLDValueObjectKeywords).
		if ld.IsKeyword(key) {
			return fmt.Errorf("linked-data signature does not cover the document: top-level %s", key)
		}
		if keyword, ok := activityPubJSONLDEmbeddedKeyword(value); ok {
			return fmt.Errorf("linked-data signature does not cover the document: embedded %s", keyword)
		}
		unsigned[key] = value
	}
	input, err := activityPubJSONLDSanitize(unsigned)
	if err != nil {
		return err
	}
	options := ld.NewJsonLdOptions("")
	// A compacted document only uses Paon's own contexts, which are preloaded.
	options.DocumentLoader = newActivityPubJSONLDDocumentLoader(true)
	expanded, err := ld.NewJsonLdProcessor().Expand(input, options)
	if err != nil {
		return fmt.Errorf("expand linked-data signature document: %w", err)
	}
	if err := activityPubJSONLDUncanonicalizedContent(expanded); err != nil {
		return fmt.Errorf("linked-data signature does not cover the document: %w", err)
	}
	if err := activityPubJSONLDUnsignedPlacement(expanded); err != nil {
		return fmt.Errorf("linked-data signature does not cover the document: %w", err)
	}
	return nil
}

// activityPubJSONLDCompactedKeywords are the only JSON-LD keywords that
// compacting with Paon's context writes below the top level of a document:
// those of value objects, list objects and indexes. Node objects get the "id"
// and "type" aliases instead of "@id" and "@type", and an embedded context of
// the original document is removed.
var activityPubJSONLDCompactedKeywords = []string{"@value", "@language", "@direction", "@list", "@index"}

// activityPubJSONLDValueObjectKeywords may only appear in a compacted map that
// has "@value". Compaction writes them inside value objects; it also keeps one
// a sender put on a node object, but json-gold cannot canonicalize such a
// document anyway. A map with "@language" but no "@value", such as a
// compacted "_:@language" blank node property, re-expands to null: it is
// neither canonicalized nor seen by activityPubJSONLDUncanonicalizedContent,
// yet Paon reads it, e.g. as a truthy suspended or in place of the signed
// value behind it.
var activityPubJSONLDValueObjectKeywords = []string{"@language", "@direction"}

// activityPubJSONLDKeywordLike matches the keys of the keyword form that
// JSON-LD 1.1 reserves without defining them ("@" followed by letters,
// json-gold's ignoredKeywordPattern). Expansion drops them like undefined
// terms, but json-gold also compacts a blank node property such as
// "_:@unknown" to "@unknown", and that key is dropped when the compacted
// document is re-expanded: the node object holding it looks bare to
// activityPubJSONLDUnsignedPlacement while Paon reads a map, e.g. a decorated
// mention href, avatar url or attachment url Paon would drop.
var activityPubJSONLDKeywordLike = regexp.MustCompile(`^@[a-zA-Z]+$`)

// activityPubJSONLDEmbeddedKeyword returns a JSON-LD keyword entry of value,
// part of a compacted document below its top level, that compacting with
// Paon's context does not write there, also one of the keyword form that
// JSON-LD does not define (activityPubJSONLDKeywordLike).
func activityPubJSONLDEmbeddedKeyword(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if _, ok := typed["@value"]; !ok {
			for _, keyword := range activityPubJSONLDValueObjectKeywords {
				if _, ok := typed[keyword]; ok {
					return keyword, true
				}
			}
		}
		for key, child := range typed {
			if (ld.IsKeyword(key) || activityPubJSONLDKeywordLike.MatchString(key)) && !slices.Contains(activityPubJSONLDCompactedKeywords, key) {
				return key, true
			}
			if keyword, ok := activityPubJSONLDEmbeddedKeyword(child); ok {
				return keyword, true
			}
		}
	case []any:
		for _, child := range typed {
			if keyword, ok := activityPubJSONLDEmbeddedKeyword(child); ok {
				return keyword, true
			}
		}
	}
	return "", false
}

// activityPubJSONLDUncanonicalizedContent walks an expanded JSON-LD document
// and reports the first value that RDF conversion would drop.
func activityPubJSONLDUncanonicalizedContent(value any) error {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if err := activityPubJSONLDUncanonicalizedContent(item); err != nil {
				return err
			}
		}
	case map[string]any:
		if _, ok := typed["@value"]; ok {
			if language, ok := typed["@language"].(string); ok && language != "" && !activityPubJSONLDLanguageTag.MatchString(language) {
				return fmt.Errorf("literal language tag %q is not canonicalized", language)
			}
			if datatype, ok := typed["@type"].(string); ok && !activityPubJSONLDValidIRI(datatype) {
				return fmt.Errorf("literal datatype %q is not canonicalized", datatype)
			}
			return nil
		}
		for key, item := range typed {
			switch key {
			case "@id":
				id, _ := item.(string)
				if ld.IsRelativeIri(id) || !activityPubJSONLDValidIRI(id) {
					return fmt.Errorf("node identifier %q is not canonicalized", id)
				}
			case "@type":
				types, ok := item.([]any)
				if !ok {
					types = []any{item}
				}
				for _, rawType := range types {
					if typeIRI, _ := rawType.(string); !activityPubJSONLDValidIRI(typeIRI) {
						return fmt.Errorf("node type %q is not canonicalized", typeIRI)
					}
				}
			case "@list", "@set":
				if err := activityPubJSONLDUncanonicalizedContent(item); err != nil {
					return err
				}
			case "@graph", "@included", "@reverse":
				// Named graphs, included nodes and reverse properties are never
				// authenticated; see activityPubJSONLDUnsupportedKeyword.
				return fmt.Errorf("%s is not supported in a linked-data signed document", key)
			default:
				if !activityPubJSONLDCanonicalizedProperty(key) {
					continue
				}
				// attributionDomains is not exempt: Mastodon 4.3 publishes it as
				// bare domains under an @id term, which RDF conversion drops,
				// and Paon stores it as the account's attribution_domains.
				if err := activityPubJSONLDUncanonicalizedContent(item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// activityPubJSONLDUnsignedPlacement rejects an expanded document whose node
// objects describe the signed nodes in a shape Paon would read differently
// from the signed RDF dataset. RDF conversion merges all node objects with the
// same @id into one subject and keeps the statements of a node object even
// when the property holding it is dropped, e.g. an undefined term expanding to
// a blank node property. Without changing the canonicalized dataset, and so
// with the original signature intact, a relay can therefore move a signed node
// description below a dropped property while the node stays referenced where
// Paon reads it (hiding an actor's key or a mention), split one description
// over several node objects (hiding an actor's suspension or locked flag from
// the object Paon reads, or shifting poll tallies between options), put a
// bare reference in front of the description of the same node, or decorate a
// reference with entries that produce no statement. Paon, like
// Mastodon, reads values by their position in the compacted JSON tree, not
// from the merged graph. Such a document is not authenticated when
//
//   - a node with an @id and statements below a dropped property also has
//     that @id outside dropped properties, as a reference or a description;
//   - a blank node identifier is described by more than one node object, or
//     an IRI by node objects with different statements (identical copies,
//     e.g. an embedded actor repeated as attributedTo, are kept);
//   - one property holds both a bare reference to an @id and a description of
//     it;
//   - a reference outside dropped properties carries entries that produce no
//     statement (@index, undefined terms or empty arrays). The compacted
//     reference then stays a map instead of the IRI string readers such as
//     a mention's href, an attachment url or an avatar url expect, so Paon
//     would drop the signed value.
func activityPubJSONLDUnsignedPlacement(expanded []any) error {
	placement := activityPubJSONLDPlacement{
		descriptions: map[string][sha256.Size]byte{},
		digests:      map[uintptr][sha256.Size]byte{},
		parked:       map[string]bool{},
		placed:       map[string]bool{},
	}
	for _, item := range expanded {
		node, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if err := placement.walkNode(node, false, true); err != nil {
			return err
		}
	}
	for id := range placement.parked {
		if placement.placed[id] {
			return fmt.Errorf("node %q is described below a property the signature does not cover", id)
		}
	}
	return nil
}

// activityPubJSONLDPlacement records where an expanded document describes
// and references node identifiers.
type activityPubJSONLDPlacement struct {
	// descriptions holds the digest of the statements of each described
	// identifier (statements).
	descriptions map[string][sha256.Size]byte
	// digests memoizes statements for each map of the expanded document by
	// the identity of the map, which the document keeps alive and in place
	// during the walk.
	digests map[uintptr][sha256.Size]byte
	// parked holds identifiers described below a dropped property.
	parked map[string]bool
	// placed holds identifiers described or referenced outside dropped
	// properties.
	placed map[string]bool
}

// walkNode records an expanded node object; dropped tells whether a property
// it is part of is left out of the RDF dataset, and root whether it is a
// top-level node of the document.
func (p *activityPubJSONLDPlacement) walkNode(node map[string]any, dropped bool, root bool) error {
	if id, ok := node["@id"].(string); ok {
		described := activityPubJSONLDNodeHasStatements(node)
		switch {
		case !dropped:
			p.placed[id] = true
		case described:
			p.parked[id] = true
		}
		if !described && !dropped && !root && len(node) > 1 {
			return fmt.Errorf("reference %q carries entries the signature does not cover", id)
		}
		if described {
			statements := p.statements(node)
			if previous, ok := p.descriptions[id]; ok {
				if strings.HasPrefix(id, "_:") {
					return fmt.Errorf("blank node %q is described more than once", id)
				}
				if previous != statements {
					return fmt.Errorf("node %q is described more than once with different statements", id)
				}
			}
			p.descriptions[id] = statements
		}
	}
	for key, value := range node {
		if ld.IsKeyword(key) {
			continue
		}
		if err := p.walkValue(value, dropped || !activityPubJSONLDCanonicalizedProperty(key)); err != nil {
			return err
		}
	}
	return nil
}

// walkValue records the node objects of an expanded property value.
func (p *activityPubJSONLDPlacement) walkValue(value any, dropped bool) error {
	switch typed := value.(type) {
	case []any:
		return p.walkValues(typed, dropped)
	case map[string]any:
		if _, ok := typed["@value"]; ok {
			return nil
		}
		if list, ok := typed["@list"]; ok {
			items, _ := list.([]any)
			return p.walkValues(items, dropped)
		}
		return p.walkNode(typed, dropped, false)
	}
	return nil
}

// walkValues records the values of one expanded property or list.
func (p *activityPubJSONLDPlacement) walkValues(values []any, dropped bool) error {
	descriptions := map[string]bool{}
	references := map[string]bool{}
	for _, value := range values {
		node, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id, ok := node["@id"].(string)
		if !ok {
			continue
		}
		switch {
		case activityPubJSONLDNodeHasStatements(node):
			descriptions[id] = true
		case len(node) == 1:
			references[id] = true
		}
	}
	for id := range descriptions {
		if references[id] {
			return fmt.Errorf("node %q is both referenced and described in one property", id)
		}
	}
	for _, value := range values {
		if err := p.walkValue(value, dropped); err != nil {
			return err
		}
	}
	return nil
}

// activityPubJSONLDNodeHasStatements reports whether RDF conversion produces a
// statement with the expanded node object as its subject: a type or a value of
// a canonicalized property.
func activityPubJSONLDNodeHasStatements(node map[string]any) bool {
	for key, value := range node {
		if key != "@type" && !activityPubJSONLDCanonicalizedProperty(key) {
			continue
		}
		if items, ok := value.([]any); !ok || len(items) > 0 {
			return true
		}
	}
	return false
}

// statements digests an expanded value for comparing descriptions of the same
// identifier, leaving out the entries RDF conversion drops: @index, dropped
// properties and empty arrays. Equal digests stand for equal values: a map is
// digested from its sorted keys and the digests of their values, an array
// from the digests of its items in order, and any other value from its JSON
// encoding, each behind a tag of its kind. The walk compares every described
// node, including the ones nested in another description, so digesting each
// subtree anew would take time and memory quadratic in the nesting depth of a
// document that any relay can send before its signature is verified. The
// digests of maps are memoized instead, so every map is digested once.
func (p *activityPubJSONLDPlacement) statements(value any) [sha256.Size]byte {
	var digest [sha256.Size]byte
	switch typed := value.(type) {
	case map[string]any:
		identity := reflect.ValueOf(typed).Pointer()
		if memoized, ok := p.digests[identity]; ok {
			return memoized
		}
		keys := make([]string, 0, len(typed))
		for key, item := range typed {
			if key == "@index" || (!ld.IsKeyword(key) && !activityPubJSONLDCanonicalizedProperty(key)) {
				continue
			}
			if items, ok := item.([]any); ok && len(items) == 0 {
				continue
			}
			keys = append(keys, key)
		}
		slices.Sort(keys)
		hash := sha256.New()
		hash.Write([]byte{'{'})
		for _, key := range keys {
			// The JSON encoding of a key delimits it from the digest after it.
			encodedKey, _ := json.Marshal(key)
			hash.Write(encodedKey)
			item := p.statements(typed[key])
			hash.Write(item[:])
		}
		hash.Sum(digest[:0])
		p.digests[identity] = digest
	case []any:
		hash := sha256.New()
		hash.Write([]byte{'['})
		for _, item := range typed {
			itemDigest := p.statements(item)
			hash.Write(itemDigest[:])
		}
		hash.Sum(digest[:0])
	default:
		encoded, _ := json.Marshal(value)
		digest = sha256.Sum256(append([]byte{'='}, encoded...))
	}
	return digest
}

// activityPubJSONLDUnsupportedKeywords mirrors Mastodon's
// JsonLdHelper::UNSUPPORTED_JSONLD_KEYWORDS.
var activityPubJSONLDUnsupportedKeywords = []string{"@graph", "@included", "@reverse"}

// activityPubJSONLDUnsupportedKeyword reports the first unsupported JSON-LD
// keyword used as a key anywhere in a compacted document, like Mastodon's
// JsonLdHelper#unsupported_jsonld_features?.
func activityPubJSONLDUnsupportedKeyword(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if slices.Contains(activityPubJSONLDUnsupportedKeywords, key) {
				return key, true
			}
			if keyword, ok := activityPubJSONLDUnsupportedKeyword(item); ok {
				return keyword, true
			}
		}
	case []any:
		for _, item := range typed {
			if keyword, ok := activityPubJSONLDUnsupportedKeyword(item); ok {
				return keyword, true
			}
		}
	}
	return "", false
}

// activityPubJSONLDCanonicalizedProperty reports whether RDF conversion keeps
// statements for property (GraphToRDF in ld/rdf_dataset.go). Other properties
// are dropped as a whole, like undefined terms.
func activityPubJSONLDCanonicalizedProperty(property string) bool {
	return !ld.IsKeyword(property) &&
		!strings.HasPrefix(property, "_:") &&
		!ld.IsRelativeIri(property) &&
		activityPubJSONLDValidIRI(property)
}

// activityPubJSONLDValidIRI mirrors json-gold's validIRI (ld/rdf_dataset.go).
func activityPubJSONLDValidIRI(iri string) bool {
	if strings.HasPrefix(iri, "http://") || strings.HasPrefix(iri, "https://") {
		return ld.IsURL(iri)
	}
	return true
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

func activityPubJSONLDNormalize(value any) (string, error) {
	return activityPubJSONLDNormalizeWithLoader(value, activityPubJSONLDDocumentLoader())
}

func activityPubJSONLDNormalizeWithLoader(value any, loader ld.DocumentLoader) (normalizedString string, err error) {
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
	options.DocumentLoader = loader
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

// activityPubFullJSONLDContextExtensions holds the extension terms of the
// context signed documents are compacted with (activityPubFullJSONLDContext).
// A key Paon reads from them must be a term of that context: otherwise the
// ActivityStreams "@vocab": "_:" compacts an unsigned blank node property such
// as "_:attributionDomains" (an undefined term) to that key. Like Mastodon
// v4.3.23 (ContextHelper#full_context), attributionDomains is defined so that
// such a property stays "_:attributionDomains" and is never read.
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
		"suspended":                 "toot:suspended",
		"attributionDomains":        map[string]any{"@id": "toot:attributionDomains", "@type": "@id"},
		"fep":                       "https://w3id.org/fep/044f#",
		"quote":                     map[string]any{"@id": "fep:quote", "@type": "@id"},
		"quoteUri":                  "toot:quoteUri",
		"votersCount":               "toot:votersCount",
		"gts":                       "https://gotosocial.org/ns#",
		"interactionPolicy":         map[string]any{"@id": "gts:interactionPolicy", "@type": "@id"},
		"canQuote":                  map[string]any{"@id": "gts:canQuote", "@type": "@id"},
		"automaticApproval":         map[string]any{"@id": "gts:automaticApproval", "@type": "@id"},
		"manualApproval":            map[string]any{"@id": "gts:manualApproval", "@type": "@id"},
		"quoteUrl":                  "as:quoteUrl",
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
	return newActivityPubJSONLDDocumentLoader(false)
}

func newActivityPubJSONLDDocumentLoader(offline bool) ld.DocumentLoader {
	loader := ld.NewCachingDocumentLoader(mastodonJSONLDDocumentLoader{client: activityPubJSONLDHTTPClient(), cache: true, offline: offline})
	activityStreams := map[string]any{"@context": activityPubActivityStreamsJSONLDContext()}
	security := map[string]any{"@context": activityPubSecurityJSONLDContext()}
	identity := map[string]any{"@context": activityPubIdentityJSONLDContext()}
	fep044f := map[string]any{"@context": activityPubFEP044FJSONLDContext()}
	toot := map[string]any{"@context": activityPubTootJSONLDContext()}
	gts := map[string]any{"@context": activityPubGTSJSONLDContext()}
	misskey := map[string]any{"@context": activityPubMisskeyJSONLDContext()}
	ostatus := map[string]any{"@context": activityPubOStatusJSONLDContext()}
	schema := map[string]any{"@context": activityPubSchemaJSONLDContext()}
	webfinger := map[string]any{"@context": activityPubWebFingerJSONLDContext()}
	// Mastodon 4.6 documents reference this context; Mastodon preloads it
	// (config/initializers/json_ld_webfinger.rb) so relayed documents still
	// compact when the remote context cannot be fetched.
	for _, uri := range []string{"http://purl.archive.org/socialweb/webfinger", "https://purl.archive.org/socialweb/webfinger"} {
		loader.AddDocument(uri, webfinger)
	}
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
	client  *http.Client
	cache   bool
	offline bool
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
	if loader.offline {
		return nil, fmt.Errorf("JSON-LD context is not cached: %q", uri)
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
		"fep":   "https://w3id.org/fep/044f#",
		"quote": map[string]any{"@id": "fep:quote", "@type": "@id"},
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
		"quoteUri":        map[string]any{"@id": "toot:quoteUri", "@type": "@id"},
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

// activityPubWebFingerJSONLDContext mirrors the term definitions Mastodon
// preloads for http://purl.archive.org/socialweb/webfinger.
func activityPubWebFingerJSONLDContext() map[string]any {
	return map[string]any{
		"webfinger": map[string]any{"@id": "https://purl.archive.org/socialweb/webfinger#webfinger", "@type": "http://www.w3.org/2001/XMLSchema#string"},
		"wf":        "https://purl.archive.org/socialweb/webfinger#",
		"xsd":       "http://www.w3.org/2001/XMLSchema#",
	}
}

func activityPubSchemaJSONLDContext() map[string]any {
	return map[string]any{
		"schema":        "http://schema.org#",
		"PropertyValue": "schema:PropertyValue",
		"value":         "schema:value",
	}
}

// activityPubActivityStreamsJSONLDContext mirrors the W3C context at
// https://www.w3.org/ns/activitystreams. Keep application extensions in
// activityPubFullJSONLDContextExtensions: adding terms here changes the RDF
// signed by remote servers, including otherwise undefined actor properties.
func activityPubActivityStreamsJSONLDContext() map[string]any {
	ctx := map[string]any{
		"@vocab": "_:",
		"xsd":    "http://www.w3.org/2001/XMLSchema#",
		"as":     "https://www.w3.org/ns/activitystreams#",
		"ldp":    "http://www.w3.org/ns/ldp#",
		"vcard":  "http://www.w3.org/2006/vcard/ns#",
		"id":     "@id",
		"type":   "@type",
	}
	for _, term := range []string{
		"Accept", "Activity", "IntransitiveActivity", "Add", "Announce", "Application", "Arrive", "Article", "Audio", "Block", "Collection", "CollectionPage", "Relationship", "Create", "Delete", "Dislike", "Document", "Event", "Follow", "Flag", "Group", "Ignore", "Image", "Invite", "Join", "Leave", "Like", "Link", "Mention", "Note", "Object", "Offer", "OrderedCollection", "OrderedCollectionPage", "Organization", "Page", "Person", "Place", "Profile", "Question", "Reject", "Remove", "Service", "TentativeAccept", "TentativeReject", "Tombstone", "Undo", "Update", "Video", "View", "Listen", "Read", "Move", "Travel", "IsFollowing", "IsFollowedBy", "IsContact", "IsMember", "content", "name", "hreflang", "mediaType", "rel", "summary", "units", "preferredUsername", "source",
	} {
		ctx[term] = "as:" + term
	}
	for _, term := range []string{
		"subject", "relationship", "actor", "attributedTo", "attachment", "bcc", "bto", "cc", "context", "current", "first", "generator", "icon", "image", "inReplyTo", "items", "instrument", "last", "location", "next", "object", "oneOf", "anyOf", "origin", "prev", "preview", "replies", "result", "audience", "partOf", "tag", "target", "to", "url", "href", "describes", "formerType", "outbox", "following", "followers", "streams", "endpoints", "uploadMedia", "proxyUrl", "liked", "oauthAuthorizationEndpoint", "oauthTokenEndpoint", "provideClientKey", "signClientKey", "sharedInbox", "Public", "likes", "shares", "alsoKnownAs",
	} {
		ctx[term] = map[string]any{"@id": "as:" + term, "@type": "@id"}
	}
	for _, term := range []string{"closed", "endTime", "published", "startTime", "updated", "deleted"} {
		ctx[term] = map[string]any{"@id": "as:" + term, "@type": "xsd:dateTime"}
	}
	for _, term := range []string{"accuracy", "altitude", "latitude", "longitude", "radius"} {
		ctx[term] = map[string]any{"@id": "as:" + term, "@type": "xsd:float"}
	}
	for _, term := range []string{"height", "startIndex", "totalItems", "width"} {
		ctx[term] = map[string]any{"@id": "as:" + term, "@type": "xsd:nonNegativeInteger"}
	}
	for _, term := range []string{"duration"} {
		ctx[term] = map[string]any{"@id": "as:" + term, "@type": "xsd:duration"}
	}
	ctx["orderedItems"] = map[string]any{"@id": "as:items", "@type": "@id", "@container": "@list"}
	ctx["contentMap"] = map[string]any{"@id": "as:content", "@container": "@language"}
	ctx["nameMap"] = map[string]any{"@id": "as:name", "@container": "@language"}
	ctx["summaryMap"] = map[string]any{"@id": "as:summary", "@container": "@language"}
	ctx["inbox"] = map[string]any{"@id": "ldp:inbox", "@type": "@id"}
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
