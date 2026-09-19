package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

// A missing signature can be recovered for an Announce by retrieving the
// activity itself from its actor's origin. Never use this path for a supplied
// signature, including one discarded during failed JSON-LD compaction.
func activityPubUnsignedAnnounce(body []byte, payload activityPayload) bool {
	if payload.Type != "Announce" || payload.Signature.Present {
		return false
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return false
	}
	if _, graph := document["@graph"]; graph {
		return false
	}
	for key := range document {
		if activityPubJSONLDKeyMatchesTerm(key, "signature") {
			return false
		}
	}
	return true
}

func (s *Server) activityPubResolveRelayedAnnounce(ctx context.Context, claimed activityPayload, relay *models.Account) ([]byte, *models.Account, error) {
	accepted, err := s.activityPubRequestedThroughRelay(*relay)
	if err != nil {
		return nil, nil, err
	}
	if !accepted {
		return nil, nil, fmt.Errorf("linked-data signature is missing; unsigned Announce sender is not an accepted relay")
	}
	activityID := activityPayloadIDValueOrID(claimed)
	actorURI := activityPayloadFetchActorURI(claimed)
	if !activityPubSameHTTPSOrigin(activityID, actorURI) || s.localActivityURI(actorURI) {
		return nil, nil, fmt.Errorf("unsigned Announce activity and actor must belong to the same remote HTTPS origin")
	}
	if disallowed, err := s.remoteActivityDomainNotAllowed(activityID); err != nil || disallowed {
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errActivitySignatureDomainNotAllowed
	}
	signer := s.activityFetchSigner(nil)
	body, err := s.fetchActivityPubCanonicalAnnounce(ctx, claimed, signer)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch canonical unsigned Announce: %w", err)
	}
	actor, err := s.activityActorForURI(actorURI)
	if err != nil {
		return nil, nil, err
	}
	if actor == nil || actor.Local() || actor.URI != actorURI {
		return nil, nil, fmt.Errorf("canonical Announce actor could not be resolved")
	}
	return body, actor, nil
}

func activityPubSameHTTPSOrigin(left, right string) bool {
	parse := func(raw string) (*url.URL, bool) {
		parsed, err := url.Parse(raw)
		return parsed, err == nil && strings.TrimSpace(raw) == raw && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.Fragment == "" && parsed.Opaque == ""
	}
	l, lok := parse(left)
	r, rok := parse(right)
	if !lok || !rok || !strings.EqualFold(l.Hostname(), r.Hostname()) {
		return false
	}
	port := func(u *url.URL) string {
		if u.Port() == "" {
			return "443"
		}
		return u.Port()
	}
	return port(l) == port(r)
}

func (s *Server) fetchActivityPubCanonicalAnnounce(ctx context.Context, claimed activityPayload, signer *models.Account) ([]byte, error) {
	activityID := activityPayloadIDValueOrID(claimed)
	actorURI := activityPayloadFetchActorURI(claimed)
	if claimed.Type != "Announce" || !activityPubSameHTTPSOrigin(activityID, actorURI) || claimed.Object.ID == "" {
		return nil, fmt.Errorf("invalid canonical Announce identity")
	}
	parsed, _ := url.Parse(activityID)
	if !activityFetchHostAllowed(parsed.Hostname()) {
		return nil, fmt.Errorf("remote host is not allowed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, activityID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", activityDereferencerAcceptHeader)
	req.Header.Set("User-Agent", paonUserAgent(s.cfg))
	req.Header.Set("Accept-Encoding", "gzip")
	if signer != nil && signer.PrivateKey.Valid && strings.TrimSpace(signer.PrivateKey.String) != "" {
		if err := s.signActivityPubFetchRequest(req, *signer); err != nil {
			return nil, err
		}
	}
	client := activityHTTPClientForActivityFetch(s, signer)
	checkRedirect := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !activityPubSameHTTPSOrigin(activityID, request.URL.String()) {
			return fmt.Errorf("canonical Announce redirect leaves actor origin")
		}
		return checkRedirect(request, via)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, activityFetchHTTPError{StatusCode: resp.StatusCode, URL: activityID}
	}
	if !activityJSONContentType(resp.Header.Get("Content-Type")) {
		return nil, fmt.Errorf("unsupported activity content type")
	}
	if resp.ContentLength > maxActivityResourceBodySize {
		return nil, fmt.Errorf("remote activity body too large")
	}
	reader := io.Reader(resp.Body)
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		compressed, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer compressed.Close()
		reader = compressed
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxActivityResourceBodySize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxActivityResourceBodySize {
		return nil, fmt.Errorf("remote activity body too large")
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, err
	}
	if _, graph := document["@graph"]; graph {
		return nil, fmt.Errorf("canonical Announce must be a standalone activity")
	}
	fetched, err := parseActivityResourcePayload(body)
	if err != nil {
		return nil, err
	}
	if fetched.Type != "Announce" || activityPayloadIDValueOrID(fetched) != activityID || activityPayloadFetchActorURI(fetched) != actorURI || fetched.Object.ID != claimed.Object.ID {
		return nil, fmt.Errorf("canonical Announce identity does not match relayed activity")
	}
	// HTTPS establishes this copy's authority. Do not propagate any unverified
	// LD signature returned with it as proof to a subsequent recipient.
	for key := range document {
		if activityPubJSONLDKeyMatchesTerm(key, "signature") {
			delete(document, key)
		}
	}
	return json.Marshal(document)
}
