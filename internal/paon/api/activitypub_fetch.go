package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

const maxActivityFetchDepth = 4
const maxActivityResourceBodySize = 1 << 20
const activityResourceAcceptHeader = `application/activity+json, application/ld+json; profile="https://www.w3.org/ns/activitystreams", text/html;q=0.1`
const activityDereferencerAcceptHeader = `application/activity+json, application/ld+json`
const remoteStatusDiscoveriesPerRequest = 1000
const remoteActorDiscoveriesPerRequest = 400
const remoteActorSubdomainsRateLimit = 10
const remoteStatusDiscoveryTTL = 5 * time.Minute

type fetchedActivityResource struct {
	body        []byte
	contentType string
	linkHeader  string
}

type activityFetchHTTPError struct {
	StatusCode int
	URL        string
}

func (e activityFetchHTTPError) Error() string {
	return fmt.Sprintf("failed to fetch remote activity: %d", e.StatusCode)
}

func activityFetchStatus(err error) (int, bool) {
	var httpErr activityFetchHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode, true
	}
	return 0, false
}

func activityFetchGone(err error) bool {
	status, ok := activityFetchStatus(err)
	return ok && status == http.StatusGone
}

func activityFetchUnsalvageable(err error) bool {
	status, ok := activityFetchStatus(err)
	return ok && activityPubDeliveryResponseErrorUnsalvageable(status)
}

func activityFetchWorkerError(err error) error {
	if err == nil || activityFetchUnsalvageable(err) {
		return nil
	}
	return err
}

func (s *Server) fetchRemoteStatusFromActivityURI(uri string) (*models.Status, error) {
	return s.fetchRemoteStatusFromActivityURIForRequest(uri, "", "")
}

func (s *Server) fetchRemoteStatusFromActivityURIForRequest(uri string, expectedActorURI string, requestID string) (*models.Status, error) {
	return s.fetchRemoteStatusFromActivityURIForRequestWithSigner(uri, expectedActorURI, requestID, nil)
}

func (s *Server) fetchRemoteStatusFromActivityURIForRequestWithSigner(uri string, expectedActorURI string, requestID string, signer *models.Account) (*models.Status, error) {
	if strings.TrimSpace(uri) == "" || strings.TrimSpace(uri) != uri || s.db == nil {
		return nil, nil
	}
	expectedURI := activityPubFetchExpectedID(uri)
	if disallowed, err := s.remoteActivityDomainNotAllowed(expectedURI); err != nil || disallowed {
		return nil, err
	}
	requestID = remoteStatusDiscoveryRequestID(requestID, uri)
	payload, err := s.fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentAndSigner(uri, expectedURI, paonUserAgent(s.cfg), signer)
	if err != nil {
		return nil, nil
	}
	if payload.Type == "Announce" {
		return s.fetchRemoteAnnounceStatus(uri, payload, expectedActorURI, requestID)
	}
	return s.processFetchedRemoteStatusPayload(uri, payload, expectedActorURI, requestID)
}

func (s *Server) fetchRemoteStatusFromResolvableURL(uri string, current ...*models.Account) (*models.Status, error) {
	return s.fetchRemoteStatusFromResolvableURLForRequest(uri, "", current...)
}

func (s *Server) fetchRemoteStatusFromResolvableURLForRequest(uri string, requestID string, current ...*models.Account) (*models.Status, error) {
	if strings.TrimSpace(uri) == "" || strings.TrimSpace(uri) != uri || s.db == nil {
		return nil, nil
	}
	account := firstAccount(current)
	if disallowed, err := s.remoteActivityDomainNotAllowed(activityPubFetchExpectedID(uri)); err != nil || disallowed {
		return nil, err
	}
	requestID = remoteStatusDiscoveryRequestID(requestID, uri)
	payload, err := s.fetchActivityResourcePayloadWithUserAgentAndSigner(uri, paonUserAgent(s.cfg), s.activityFetchResourceServiceSigner())
	if err != nil {
		if status, ok := activityFetchStatus(err); ok && (status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound) {
			return s.resolveKnownRemoteStatusFromDB(uri, account)
		}
		return nil, nil
	}
	if canonical := activityResourceCanonicalID(payload); canonical != "" {
		if disallowed, err := s.remoteActivityDomainNotAllowed(activityPubFetchExpectedID(canonical)); err != nil || disallowed {
			return nil, err
		}
	}
	if payload.Type == "Announce" {
		return s.fetchRemoteAnnounceStatus(uri, payload, "", requestID)
	}
	return s.processFetchedRemoteStatusPayload(uri, payload, "", requestID)
}

func (s *Server) fetchVisibleRemoteStatusFromResolvableURL(uri string, current *models.Account) (*models.Status, error) {
	status, err := s.fetchRemoteStatusFromResolvableURL(uri, current)
	if err != nil || status == nil || status.ID == 0 {
		return status, err
	}
	return s.visibleResolvedStatus(current, status)
}

func (s *Server) visibleResolvedStatus(account *models.Account, status *models.Status) (*models.Status, error) {
	if status == nil || status.ID == 0 {
		return status, nil
	}
	visible, err := s.findVisibleStatusForAccount(account, strconv.FormatInt(status.ID, 10))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return visible, err
}

func (s *Server) processFetchedRemoteStatusPayload(uri string, payload activityPayload, expectedActorURI string, requestID string) (*models.Status, error) {
	expectedURI := activityPubFetchExpectedID(uri)
	note := payload.Object
	actorURI := activityPayloadFetchActorURI(payload)
	objectURI := note.ID
	trustURI, trustPresent := activityFetchedStatusTrustURI(payload)
	if payload.ObjectReference && objectURI != "" {
		activityID := activityPayloadIDValueOrID(payload)
		if activityID == "" {
			return nil, nil
		}
		trustURI = activityID
		trustPresent = true
	} else if !activityObjectIsStatus(note) {
		return nil, nil
	}
	if actorURI == "" || objectURI == "" || !trustPresent || !activityAttributionTrusted(trustURI, actorURI) || s.localActivityURI(actorURI) {
		return nil, nil
	}
	if strings.TrimSpace(expectedActorURI) != "" && actorURI != expectedActorURI {
		return nil, nil
	}
	if note.AttributedTo == "" {
		note.AttributedTo = actorURI
		payload.Object = note
	}
	actor, err := s.activityActorForURIForRequest(actorURI, requestID)
	if err != nil || actor == nil || actor.SuspendedAt.Valid {
		return nil, nil
	}
	if !s.remoteStatusDiscoveryAllowed(requestID) {
		return nil, nil
	}
	if payload.ObjectReference {
		if err := s.processFetchedActivityPubReferencedStatusForRequest(payload, actor, requestID); err != nil {
			return nil, err
		}
		return s.statusFromActivityURI(objectURI)
	}
	if err := s.processFetchedActivityPubStatusForRequest(payload, actor, requestID); err != nil {
		return nil, err
	}
	return s.statusFromActivityURI(firstNonEmpty(note.ID, expectedURI))
}

func activityPubFetchExpectedID(uri string) string {
	expected := activityURIFromBearcap(uri)
	if strings.TrimSpace(expected) == "" {
		return uri
	}
	return expected
}

func activityFetchedStatusTrustURI(payload activityPayload) (string, bool) {
	if payload.ObjectDocument {
		return payload.Object.ID, payload.Object.ID != ""
	}
	activityID := activityPayloadIDValueOrID(payload)
	return activityID, activityID != ""
}

func activityPayloadFetchActorURI(payload activityPayload) string {
	return firstNonEmpty(payload.ActorRaw, payload.Actor)
}

func remoteStatusDiscoveryRequestID(requestID string, uri string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID != "" {
		return requestID
	}
	return fmt.Sprintf("%d-status-%s", time.Now().UTC().Unix(), uri)
}

func remoteStatusDiscoveryRedisKey(cfgPrefix string, requestID string) string {
	return cfgPrefix + "status_discovery_per_request:" + requestID
}

func remoteActorDiscoveryRedisKey(cfgPrefix string, requestID string) string {
	return cfgPrefix + "discovery_per_request:" + requestID
}

func remoteActorSubdomainRedisKey(cfgPrefix string, domain string) string {
	base := registrableDomain(domain)
	if base == "" {
		return ""
	}
	return cfgPrefix + "unique_subdomains_for:" + base
}

func (s *Server) remoteStatusDiscoveryAllowed(requestID string) bool {
	if s == nil || strings.TrimSpace(requestID) == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	key := remoteStatusDiscoveryRedisKey(redisConfig(s.cfg).prefix, requestID)
	value, err := s.redisCommand(ctx, "INCR", key)
	if err != nil {
		return true
	}
	_, _ = s.redisCommand(ctx, "EXPIRE", key, fmt.Sprintf("%.0f", remoteStatusDiscoveryTTL.Seconds()))
	return redisInt(value) <= remoteStatusDiscoveriesPerRequest
}

func (s *Server) remoteActorDiscoveryAllowed(requestID string) bool {
	if s == nil || strings.TrimSpace(requestID) == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	key := remoteActorDiscoveryRedisKey(redisConfig(s.cfg).prefix, requestID)
	value, err := s.redisCommand(ctx, "INCR", key)
	if err != nil {
		return true
	}
	_, _ = s.redisCommand(ctx, "EXPIRE", key, fmt.Sprintf("%.0f", remoteStatusDiscoveryTTL.Seconds()))
	return redisInt(value) <= remoteActorDiscoveriesPerRequest
}

func (s *Server) remoteActorSubdomainAllowed(domain string) bool {
	if s == nil {
		return true
	}
	key := remoteActorSubdomainRedisKey(redisConfig(s.cfg).prefix, domain)
	if key == "" {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	value, err := s.redisCommand(ctx, "PFCOUNT", key)
	if err != nil {
		return true
	}
	return redisInt(value) < remoteActorSubdomainsRateLimit
}

func (s *Server) fetchRemoteAnnounceStatus(uri string, payload activityPayload, expectedActorURI string, requestID string) (*models.Status, error) {
	actorURI := activityPayloadFetchActorURI(payload)
	targetURI := activityAnnounceTargetURI(payload)
	activityID := activityPayloadIDValueOrID(payload)
	if actorURI == "" || targetURI == "" || activityID == "" || !activityAttributionTrusted(activityID, actorURI) || s.localActivityURI(actorURI) {
		return nil, nil
	}
	if strings.TrimSpace(expectedActorURI) != "" && actorURI != expectedActorURI {
		return nil, nil
	}
	if s.localActivityURI(targetURI) {
		return s.statusFromActivityURI(targetURI)
	}
	actor, err := s.activityActorForURIForRequest(actorURI, requestID)
	if err != nil || actor == nil || actor.SuspendedAt.Valid {
		return nil, err
	}
	target, err := s.statusFromActivityURI(targetURI)
	if err != nil {
		return nil, err
	}
	if target == nil {
		if activityObjectIsStatus(payload.Object) && payload.Object.ID != "" {
			if err := s.processFetchedActivityPubStatusObjectForRequest(payload.Object, requestID); err != nil {
				return nil, err
			}
		} else if _, err := s.fetchRemoteStatusFromActivityURIForRequest(targetURI, "", requestID); err != nil {
			return nil, err
		}
	}
	if !s.remoteStatusDiscoveryAllowed(requestID) {
		return nil, nil
	}
	if err := s.processActivityPubAnnounce(payload, actor, nil, activityPubProcessingOptions{RequestID: requestID}); err != nil {
		return nil, err
	}
	return s.statusFromActivityURI(activityAnnounceURI(activityID, actor, targetURI))
}

func (s *Server) processFetchedActivityPubNote(note activityObject) error {
	return s.processFetchedActivityPubStatusObjectForRequest(note, "")
}

func (s *Server) processFetchedActivityPubNoteForRequest(note activityObject, requestID string) error {
	return s.processFetchedActivityPubStatusObjectForRequest(note, requestID)
}

func (s *Server) processFetchedActivityPubStatusObjectForRequest(note activityObject, requestID string) error {
	if !activityObjectIsStatus(note) {
		return nil
	}
	actorURI := firstNonEmpty(note.AttributedToRaw, note.AttributedTo)
	if actorURI == "" || !activityAttributionTrusted(note.ID, actorURI) || s.localActivityURI(actorURI) {
		return nil
	}
	actor, err := s.activityActorForURIForRequest(actorURI, remoteStatusDiscoveryRequestID(requestID, note.ID))
	if err != nil || actor == nil || actor.SuspendedAt.Valid {
		return err
	}
	return s.processFetchedActivityPubStatusForRequest(activityPayload{Type: "Create", Actor: note.AttributedTo, ActorRaw: actorURI, Object: note}, actor, requestID)
}

func (s *Server) processFetchedActivityPubStatus(payload activityPayload, actor *models.Account) error {
	return s.processFetchedActivityPubStatusForRequest(payload, actor, "")
}

func (s *Server) processFetchedActivityPubStatusForRequest(payload activityPayload, actor *models.Account, requestID string) error {
	if s == nil || s.db == nil || actor == nil || actor.ID == 0 {
		return nil
	}
	note := payload.Object
	if note.ID == "" {
		return nil
	}
	var existing models.Status
	err := s.db.Where("uri = ? AND account_id = ? AND deleted_at IS NULL", note.ID, actor.ID).First(&existing).Error
	if err == nil {
		payload.Type = "Update"
		return s.processActivityPubUpdate(payload, actor, nil, nil, activityPubProcessingOptions{RequestID: requestID})
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	payload.Fetch = true
	return s.processActivityPubCreateNote(payload, actor, nil, nil, activityPubProcessingOptions{RequestID: requestID})
}

func (s *Server) processFetchedActivityPubReferencedStatus(payload activityPayload, actor *models.Account) error {
	return s.processFetchedActivityPubReferencedStatusForRequest(payload, actor, "")
}

func (s *Server) processFetchedActivityPubReferencedStatusForRequest(payload activityPayload, actor *models.Account, requestID string) error {
	if s == nil || actor == nil || payload.Object.ID == "" {
		return nil
	}
	rawObjectURI := payload.Object.ID
	if len(payload.ObjectIDRaws) > 0 && strings.TrimSpace(payload.ObjectIDRaws[0]) != "" {
		rawObjectURI = payload.ObjectIDRaws[0]
	}
	fetchURI, expectedURI := activityPubDereferenceFetchURI(rawObjectURI)
	if fetchURI == "" || expectedURI == "" {
		return nil
	}
	signer, err := s.activityPubSignedFetchAccount(actor, nil, payload.To, payload.CC)
	if err != nil {
		return err
	}
	dereferenced, err := s.fetchActivityDereferencerPayloadStrictWithExpectedID(fetchURI, expectedURI, paonUserAgent(s.cfg), signer)
	if err != nil || !activityObjectIsStatus(dereferenced.Object) {
		return err
	}
	if dereferenced.Actor != "" && actor.URI != "" && dereferenced.Actor != actor.URI {
		return nil
	}
	if dereferenced.Object.AttributedTo == "" {
		dereferenced.Object.AttributedTo = actor.URI
	}
	if !activityNoteBelongsToActor(dereferenced.Object, actor) {
		return nil
	}
	dereferenced.ID = firstNonEmpty(payload.ID, dereferenced.ID)
	dereferenced.Actor = firstNonEmpty(payload.Actor, dereferenced.Actor, actor.URI)
	dereferenced.ActorRaw = firstNonEmpty(payload.ActorRaw, dereferenced.ActorRaw, dereferenced.Actor)
	dereferenced.Published = firstNonEmpty(payload.Published, dereferenced.Published)
	dereferenced.To = firstNonEmptyStringSlice(payload.To, dereferenced.To)
	dereferenced.CC = firstNonEmptyStringSlice(payload.CC, dereferenced.CC)
	return s.processFetchedActivityPubStatusForRequest(dereferenced, actor, requestID)
}

func activityAnnounceTargetURI(payload activityPayload) string {
	return payload.Object.ID
}

func fetchActivityResourcePayload(uri string) (activityPayload, error) {
	return fetchActivityResourcePayloadWithUserAgent(uri, "")
}

func fetchActivityResourcePayloadWithUserAgent(uri string, userAgent string) (activityPayload, error) {
	return fetchActivityResourcePayloadDepthWithFetcher(uri, 0, userAgent, fetchActivityResourceWithMetadataAndUserAgent)
}

func (s *Server) fetchActivityResourcePayloadWithUserAgentAndSigner(uri string, userAgent string, signer *models.Account) (activityPayload, error) {
	signer = s.activityFetchSigner(signer)
	fetcher := func(uri string, userAgent string) (fetchedActivityResource, error) {
		return fetchActivityResourceWithMetadataAndUserAgentSigned(uri, userAgent, s, signer)
	}
	return fetchActivityResourcePayloadDepthWithFetcher(uri, 0, userAgent, fetcher)
}

func (s *Server) fetchActivityResourcePayloadStrictWithUserAgentAndSigner(uri string, userAgent string, signer *models.Account) (activityPayload, error) {
	return s.fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentAndSigner(uri, uri, userAgent, signer)
}

func (s *Server) fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentAndSigner(uri string, expectedID string, userAgent string, signer *models.Account) (activityPayload, error) {
	return s.fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentSignerAndAccept(uri, expectedID, userAgent, signer, activityResourceAcceptHeader)
}

func (s *Server) fetchActivityResourcePayloadStrictWithExpectedIDAndUserAgentSignerAndAccept(uri string, expectedID string, userAgent string, signer *models.Account, acceptHeader string) (activityPayload, error) {
	signer = s.activityFetchSigner(signer)
	fetcher := func(uri string, userAgent string) (fetchedActivityResource, error) {
		return fetchActivityResourceWithMetadataAndUserAgentSignedWithAccept(uri, userAgent, s, signer, acceptHeader)
	}
	return fetchActivityResourcePayloadStrictDepthWithExpectedIDAndFetcher(uri, expectedID, 0, userAgent, fetcher)
}

func (s *Server) fetchActivityDereferencerPayloadStrictWithExpectedID(uri string, expectedID string, userAgent string, signer *models.Account) (activityPayload, error) {
	signer = s.activityFetchSigner(signer)
	fetcher := func(fetchURI string, userAgent string) (fetchedActivityResource, error) {
		return fetchActivityResourceWithMetadataAndUserAgentSignedWithAccept(fetchURI, userAgent, s, signer, activityDereferencerAcceptHeader)
	}
	return fetchActivityDereferencerPayloadStrictDepthWithExpectedIDAndFetcher(uri, expectedID, 0, userAgent, fetcher)
}

func (s *Server) activityFetchSigner(signer *models.Account) *models.Account {
	if signer != nil || s == nil || s.db == nil {
		return signer
	}
	representative, err := s.representativeActivityPubAccount()
	if err != nil {
		return nil
	}
	return representative
}

func (s *Server) activityFetchResourceServiceSigner() *models.Account {
	if s == nil || railsDevelopmentEnv(s.cfg) {
		return nil
	}
	return s.activityFetchSigner(nil)
}

func fetchActivityResourcePayloadDepth(uri string, depth int, userAgent string) (activityPayload, error) {
	return fetchActivityResourcePayloadDepthWithFetcher(uri, depth, userAgent, fetchActivityResourceWithMetadataAndUserAgent)
}

type activityResourceFetcher func(uri string, userAgent string) (fetchedActivityResource, error)

func fetchActivityResourcePayloadDepthWithFetcher(uri string, depth int, userAgent string, fetcher activityResourceFetcher) (activityPayload, error) {
	if depth > maxActivityFetchDepth {
		return activityPayload{}, fmt.Errorf("remote activity fetch depth exceeded")
	}
	resource, err := fetcher(uri, userAgent)
	if err != nil {
		return activityPayload{}, err
	}
	if !activityJSONContentType(resource.contentType) {
		if alternate := discoverActivityAlternateURI(uri, resource); alternate != "" && !strings.EqualFold(alternate, uri) {
			return fetchActivityResourcePayloadDepthWithFetcher(alternate, depth+1, userAgent, fetcher)
		}
		return activityPayload{}, fmt.Errorf("unsupported activity content type")
	}
	payload, err := parseActivityResourcePayload(resource.body)
	if err != nil {
		return activityPayload{}, err
	}
	if canonical := activityResourceCanonicalID(payload); canonical != "" && canonical != uri {
		if depth == 0 {
			return fetchActivityResourcePayloadDepthWithFetcher(canonical, depth+1, userAgent, fetcher)
		}
		return activityPayload{}, fmt.Errorf("remote activity id mismatch")
	}
	return expandFetchedActivityObjectWithFetcher(uri, payload, depth, userAgent, fetcher)
}

func fetchActivityResourcePayloadStrictDepthWithFetcher(uri string, depth int, userAgent string, fetcher activityResourceFetcher) (activityPayload, error) {
	return fetchActivityResourcePayloadStrictDepthWithExpectedIDAndFetcher(uri, uri, depth, userAgent, fetcher)
}

func fetchActivityResourcePayloadStrictDepthWithExpectedIDAndFetcher(uri string, expectedID string, depth int, userAgent string, fetcher activityResourceFetcher) (activityPayload, error) {
	if depth > maxActivityFetchDepth {
		return activityPayload{}, fmt.Errorf("remote activity fetch depth exceeded")
	}
	resource, err := fetcher(uri, userAgent)
	if err != nil {
		return activityPayload{}, err
	}
	if !activityJSONContentType(resource.contentType) {
		return activityPayload{}, fmt.Errorf("unsupported activity content type")
	}
	payload, err := parseActivityResourcePayload(resource.body)
	if err != nil {
		return activityPayload{}, err
	}
	if canonical := activityResourceCanonicalID(payload); canonical == "" || canonical != expectedID {
		return activityPayload{}, fmt.Errorf("remote activity id mismatch")
	}
	return expandFetchedActivityObjectStrictWithFetcher(expectedID, payload, depth, userAgent, fetcher)
}

func fetchActivityDereferencerPayloadStrictDepthWithExpectedIDAndFetcher(uri string, expectedID string, depth int, userAgent string, fetcher activityResourceFetcher) (activityPayload, error) {
	if depth > maxActivityFetchDepth {
		return activityPayload{}, fmt.Errorf("remote activity fetch depth exceeded")
	}
	resource, err := fetcher(uri, userAgent)
	if err != nil {
		if status, ok := activityFetchStatus(err); ok && activityDereferencerHTTPStatusIgnoredLikeRails(status) {
			return activityPayload{}, nil
		}
		return activityPayload{}, err
	}
	payload, err := parseActivityDereferencerResourcePayload(resource.body)
	if err != nil {
		return activityPayload{}, err
	}
	if canonical := activityResourceCanonicalID(payload); canonical == "" || canonical != expectedID {
		return activityPayload{}, fmt.Errorf("remote activity id mismatch")
	}
	return expandFetchedActivityObjectStrictWithFetcher(expectedID, payload, depth, userAgent, fetcher)
}

func activityDereferencerHTTPStatusIgnoredLikeRails(status int) bool {
	if status >= 200 && status < 300 {
		return true
	}
	return status == http.StatusNotImplemented || (status >= 400 && status < 500 && status != http.StatusUnauthorized && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests)
}

func fetchActivityResource(uri string) ([]byte, error) {
	resource, err := fetchActivityResourceWithMetadata(uri)
	if err != nil {
		return nil, err
	}
	return resource.body, nil
}

func fetchActivityResourceWithMetadata(uri string) (fetchedActivityResource, error) {
	return fetchActivityResourceWithMetadataAndUserAgent(uri, "")
}

func fetchActivityResourceWithMetadataAndUserAgent(uri string, userAgent string) (fetchedActivityResource, error) {
	return fetchActivityResourceWithMetadataAndUserAgentSigned(uri, userAgent, nil, nil)
}

func fetchActivityResourceWithMetadataAndUserAgentSigned(uri string, userAgent string, s *Server, signer *models.Account) (fetchedActivityResource, error) {
	return fetchActivityResourceWithMetadataAndUserAgentSignedWithAccept(uri, userAgent, s, signer, activityResourceAcceptHeader)
}

func fetchActivityResourceWithMetadataAndUserAgentSignedWithAccept(uri string, userAgent string, s *Server, signer *models.Account, acceptHeader string) (fetchedActivityResource, error) {
	if strings.TrimSpace(uri) == "" || strings.TrimSpace(uri) != uri {
		return fetchedActivityResource{}, fmt.Errorf("remote host is not allowed")
	}
	fetchURI, bearerToken, hasBearerToken := activityBearcapFetchURI(uri)
	parsed, err := url.Parse(fetchURI)
	if err != nil || parsed.Host == "" || !activityFetchHostAllowed(parsed.Hostname()) {
		return fetchedActivityResource{}, fmt.Errorf("remote host is not allowed")
	}
	req, err := http.NewRequest(http.MethodGet, fetchURI, nil)
	if err != nil {
		return fetchedActivityResource{}, err
	}
	if strings.TrimSpace(acceptHeader) == "" {
		acceptHeader = activityResourceAcceptHeader
	}
	req.Header.Set("Accept", acceptHeader)
	if strings.TrimSpace(userAgent) != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	if hasBearerToken {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	if s != nil && signer != nil && signer.PrivateKey.Valid && strings.TrimSpace(signer.PrivateKey.String) != "" {
		if err := s.signActivityPubFetchRequest(req, *signer); err != nil {
			return fetchedActivityResource{}, err
		}
	}
	resp, err := activityHTTPClientForActivityFetch(s, signer).Do(req)
	if err != nil {
		return fetchedActivityResource{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fetchedActivityResource{}, activityFetchHTTPError{StatusCode: resp.StatusCode, URL: fetchURI}
	}
	if resp.ContentLength > maxActivityResourceBodySize {
		return fetchedActivityResource{}, fmt.Errorf("remote activity body too large")
	}
	reader := io.Reader(resp.Body)
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fetchedActivityResource{}, err
		}
		defer gzipReader.Close()
		reader = gzipReader
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxActivityResourceBodySize+1))
	if err != nil {
		return fetchedActivityResource{}, err
	}
	if len(body) > maxActivityResourceBodySize {
		return fetchedActivityResource{}, fmt.Errorf("remote activity body too large")
	}
	return fetchedActivityResource{
		body:        body,
		contentType: resp.Header.Get("Content-Type"),
		linkHeader:  strings.Join(resp.Header.Values("Link"), ", "),
	}, nil
}

func activityBearcapFetchURI(raw string) (string, string, bool) {
	if !strings.HasPrefix(raw, "bear:") {
		return raw, "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw, "", false
	}
	uri := parsed.Query().Get("u")
	if strings.TrimSpace(uri) == "" {
		return raw, "", false
	}
	return uri, parsed.Query().Get("t"), true
}

func activityHTTPClientForActivityFetch(s *Server, signer *models.Account) *http.Client {
	if activityHTTPClient == nil {
		return nil
	}
	client := *activityHTTPClient
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		if !activityRedirectAllowed(req, via) {
			return fmt.Errorf("remote host is not allowed")
		}
		if s == nil || signer == nil || !signer.PrivateKey.Valid || strings.TrimSpace(signer.PrivateKey.String) == "" {
			return nil
		}
		req.Header.Del("Signature")
		return s.signActivityPubFetchRequest(req, *signer)
	}
	return &client
}

func parseActivityResourcePayload(body []byte) (activityPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return activityPayload{}, err
	}
	if !activityResourceSupportedContext(raw["@context"]) {
		return activityPayload{}, fmt.Errorf("unsupported activity context")
	}
	return parseActivityResourcePayloadObject(body, raw)
}

func parseActivityDereferencerResourcePayload(body []byte) (activityPayload, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return activityPayload{}, err
	}
	return parseActivityResourcePayloadObject(body, raw)
}

func parseActivityResourcePayloadObject(body []byte, raw map[string]any) (activityPayload, error) {
	switch activityJSONLDType(raw) {
	case "Create":
		payload, err := parseActivityPayload(body)
		if err != nil {
			return activityPayload{}, err
		}
		if !payload.ObjectReference && !activityObjectIsStatus(payload.Object) {
			return activityPayload{}, fmt.Errorf("unsupported activity object")
		}
		return payload, nil
	case "Announce":
		payload, err := parseActivityPayload(body)
		if err != nil {
			return activityPayload{}, err
		}
		if activityAnnounceTargetURI(payload) == "" {
			return activityPayload{}, fmt.Errorf("activity object id is missing")
		}
		return payload, nil
	case "Note":
		note := parseActivityObject(raw)
		if note.ID == "" {
			return activityPayload{}, fmt.Errorf("activity object id is missing")
		}
		return activityPayload{Type: "Create", Actor: note.AttributedTo, ActorRaw: firstNonEmpty(note.AttributedToRaw, note.AttributedTo), Object: note, ObjectDocument: true}, nil
	case "Image", "Audio", "Video", "Article", "Page", "Event", "Question":
		note := parseActivityObject(raw)
		if note.ID == "" {
			return activityPayload{}, fmt.Errorf("activity object id is missing")
		}
		return activityPayload{Type: "Create", Actor: note.AttributedTo, ActorRaw: firstNonEmpty(note.AttributedToRaw, note.AttributedTo), Object: note, ObjectDocument: true}, nil
	default:
		return activityPayload{}, fmt.Errorf("unsupported activity type")
	}
}

func activityResourceSupportedContext(value any) bool {
	const context = "https://www.w3.org/ns/activitystreams"
	switch typed := value.(type) {
	case string:
		return typed == context
	case []any:
		for _, item := range typed {
			if item == context {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func activityResourceCanonicalID(payload activityPayload) string {
	if payload.ID != "" {
		return payload.ID
	}
	return payload.Object.ID
}

func expandFetchedActivityObject(uri string, payload activityPayload, depth int, userAgent string) (activityPayload, error) {
	return expandFetchedActivityObjectWithFetcher(uri, payload, depth, userAgent, fetchActivityResourceWithMetadataAndUserAgent)
}

func expandFetchedActivityObjectWithFetcher(uri string, payload activityPayload, depth int, userAgent string, fetcher activityResourceFetcher) (activityPayload, error) {
	if payload.Type != "Create" || !payload.ObjectReference || payload.Object.Type != "" || payload.Object.ID == "" || strings.EqualFold(payload.Object.ID, uri) {
		return payload, nil
	}
	if depth >= maxActivityFetchDepth {
		return activityPayload{}, fmt.Errorf("remote activity object fetch depth exceeded")
	}
	objectPayload, err := fetchActivityResourcePayloadDepthWithFetcher(payload.Object.ID, depth+1, userAgent, fetcher)
	if err != nil {
		return activityPayload{}, err
	}
	if !activityObjectIsStatus(objectPayload.Object) {
		return activityPayload{}, fmt.Errorf("unsupported activity object")
	}
	if payload.Actor != "" {
		objectPayload.Actor = payload.Actor
	}
	if payload.ActorRaw != "" {
		objectPayload.ActorRaw = payload.ActorRaw
	}
	if objectPayload.ID == "" {
		objectPayload.ID = payload.ID
	}
	return objectPayload, nil
}

func expandFetchedActivityObjectStrictWithFetcher(uri string, payload activityPayload, depth int, userAgent string, fetcher activityResourceFetcher) (activityPayload, error) {
	if payload.Type != "Create" || !payload.ObjectReference || payload.Object.Type != "" || payload.Object.ID == "" || strings.EqualFold(payload.Object.ID, uri) {
		return payload, nil
	}
	if depth >= maxActivityFetchDepth {
		return activityPayload{}, fmt.Errorf("remote activity object fetch depth exceeded")
	}
	objectPayload, err := fetchActivityResourcePayloadStrictDepthWithFetcher(payload.Object.ID, depth+1, userAgent, fetcher)
	if err != nil {
		return activityPayload{}, err
	}
	if !activityObjectIsStatus(objectPayload.Object) {
		return activityPayload{}, fmt.Errorf("unsupported activity object")
	}
	if payload.Actor != "" {
		objectPayload.Actor = payload.Actor
	}
	if payload.ActorRaw != "" {
		objectPayload.ActorRaw = payload.ActorRaw
	}
	if objectPayload.ID == "" {
		objectPayload.ID = payload.ID
	}
	if objectPayload.Published == "" {
		objectPayload.Published = payload.Published
	}
	return objectPayload, nil
}

func discoverActivityAlternateURI(baseURL string, resource fetchedActivityResource) string {
	if link := activityAlternateURIFromLinkHeader(baseURL, resource.linkHeader); link != "" {
		return link
	}
	if !strings.Contains(strings.ToLower(resource.contentType), "html") {
		return ""
	}
	return activityAlternateURIFromHTML(baseURL, string(resource.body))
}

func activityAlternateURIFromLinkHeader(baseURL string, header string) string {
	for _, part := range splitHTTPLinkHeader(header) {
		segments := splitHTTPLinkHeaderPart(part)
		if len(segments) == 0 {
			continue
		}
		target := strings.TrimSpace(segments[0])
		if !strings.HasPrefix(target, "<") || !strings.Contains(target, ">") {
			continue
		}
		target = strings.TrimSuffix(strings.TrimPrefix(target[:strings.Index(target, ">")], "<"), ">")
		attrs := map[string]string{}
		for _, segment := range segments[1:] {
			key, value, ok := strings.Cut(segment, "=")
			if !ok {
				continue
			}
			attrs[strings.ToLower(strings.TrimSpace(key))] = unquoteHTTPLinkHeaderValue(value)
		}
		if !activityRelContains(attrs["rel"], "alternate") || !activityHTMLAlternateLinkType(attrs["type"]) {
			continue
		}
		if resolved := resolveActivityAlternateURI(baseURL, target); resolved != "" {
			return resolved
		}
	}
	return ""
}

func unquoteHTTPLinkHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		return value[1 : len(value)-1]
	}
	return value
}

func splitHTTPLinkHeader(header string) []string {
	parts := []string{}
	var current strings.Builder
	inQuote := false
	escaped := false
	for _, r := range header {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		switch r {
		case '\\':
			if inQuote {
				escaped = true
			}
			current.WriteRune(r)
		case '"':
			inQuote = !inQuote
			current.WriteRune(r)
		case ',':
			if inQuote {
				current.WriteRune(r)
				continue
			}
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

func splitHTTPLinkHeaderPart(part string) []string {
	segments := []string{}
	var current strings.Builder
	inQuote := false
	escaped := false
	for _, r := range part {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		switch r {
		case '\\':
			if inQuote {
				escaped = true
			}
			current.WriteRune(r)
		case '"':
			inQuote = !inQuote
			current.WriteRune(r)
		case ';':
			if inQuote {
				current.WriteRune(r)
				continue
			}
			segments = append(segments, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		segments = append(segments, current.String())
	}
	return segments
}

func activityAlternateURIFromHTML(baseURL string, body string) string {
	for _, link := range activityLinkTagPattern.FindAllString(body, -1) {
		attrs := htmlTagAttrs(link)
		if attrs["rel"] != "alternate" || !activityHTMLAlternateLinkType(attrs["type"]) {
			continue
		}
		if resolved := resolveActivityAlternateURI(baseURL, attrs["href"]); resolved != "" {
			return resolved
		}
	}
	return ""
}

func activityHTMLAlternateLinkType(value string) bool {
	value = strings.ReplaceAll(strings.TrimSpace(value), `\"`, `"`)
	switch value {
	case "application/activity+json", `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`:
		return true
	default:
		return false
	}
}

func activityRelContains(rel string, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	for _, value := range strings.Fields(strings.ToLower(rel)) {
		if value == needle {
			return true
		}
	}
	return false
}

func activityJSONContentType(contentType string) bool {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		parts := strings.Split(contentType, ";")
		mediaType = strings.ToLower(strings.TrimSpace(parts[0]))
		params = map[string]string{}
		for _, part := range parts[1:] {
			key, value, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			params[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), ` "'\`)
		}
	} else {
		mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	}
	if mediaType == "application/activity+json" {
		return true
	}
	if mediaType != "application/ld+json" {
		return false
	}
	for _, profile := range strings.Fields(params["profile"]) {
		if strings.EqualFold(strings.Trim(profile, ` "'\`), "https://www.w3.org/ns/activitystreams") {
			return true
		}
	}
	return false
}

func resolveActivityAlternateURI(_ string, hrefRaw string) string {
	if strings.TrimSpace(hrefRaw) == "" {
		return ""
	}
	href := html.UnescapeString(hrefRaw)
	if href != strings.TrimSpace(href) {
		return ""
	}
	parsed, err := url.Parse(href)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	return href
}

var activityLinkTagPattern = regexp.MustCompile(`(?is)<link\b[^>]*>`)

func (s *Server) activityActorForURI(actorURI string) (*models.Account, error) {
	return s.activityActorForURIForRequest(actorURI, "")
}

func (s *Server) activityActorForURIForRequest(actorURI string, requestID string) (*models.Account, error) {
	actorURI = strings.TrimSpace(actorURI)
	if actorURI == "" || s.db == nil {
		return nil, nil
	}
	if s.localActivityURI(actorURI) {
		return s.localAccountFromActivityURI(actorURI)
	}
	if disallowed, err := s.remoteActivityDomainNotAllowed(actorURI); err != nil || disallowed {
		return nil, err
	}
	actorLookupURI := actorURI
	if before, _, ok := strings.Cut(actorLookupURI, "#"); ok {
		actorLookupURI = before
	}
	var account models.Account
	err := s.db.Preload("AccountStat").Where("uri IN ? OR url = ?", []string{actorURI, actorLookupURI}, actorLookupURI).First(&account).Error
	if err == nil {
		if strings.TrimSpace(requestID) != "" && remoteActivityActorPossiblyStale(account, time.Now().UTC()) {
			actor, fetchErr := s.fetchActivityActor(actorLookupURI)
			if fetchErr != nil || actor.ID == "" || actor.PublicKey.PublicKeyPem == "" {
				return nil, nil
			}
			if err := verifyRemoteActivityActorWebFinger(actor); err != nil {
				return nil, nil
			}
			return s.upsertRemoteActivityActorForRequest(actor, requestID)
		}
		return &account, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	actor, err := s.fetchActivityActor(actorLookupURI)
	if err != nil || actor.ID == "" || actor.PublicKey.PublicKeyPem == "" {
		return nil, nil
	}
	if err := verifyRemoteActivityActorWebFinger(actor); err != nil {
		return nil, nil
	}
	return s.upsertRemoteActivityActorForRequest(actor, requestID)
}

func verifyRemoteActivityActorWebFinger(actor remoteActivityActor) error {
	host := activityPubNormalizedURIHost(actor.ID)
	if host == "" {
		return fmt.Errorf("invalid remote actor")
	}
	actorURL, err := fetchRemoteActorServiceWebFingerURL(actor.PreferredUsername, host)
	if err != nil {
		return err
	}
	if actorURL != actor.ID {
		return fmt.Errorf("webfinger response does not loop back to actor")
	}
	return nil
}

func fetchRemoteActorServiceWebFingerURL(username string, domain string) (string, error) {
	actorURL, err := fetchRemoteActorServiceWebFingerURLDepth(username, domain, 0)
	return actorURL, err
}

func fetchRemoteActorServiceWebFingerURLDepth(username string, domain string, depth int) (string, error) {
	if depth > 1 {
		return "", fmt.Errorf("too many webfinger redirects")
	}
	if username == "" || domain == "" {
		return "", fmt.Errorf("public key not found")
	}
	if !activityFetchHostAllowed(domain) {
		return "", fmt.Errorf("remote host is not allowed")
	}
	resource := "acct:" + username + "@" + domain
	doc, err := fetchActivityWebFingerDocument(activityWebFingerURL(domain, resource))
	if status, ok := activityFetchStatus(err); ok && status == http.StatusNotFound {
		fallbackURL, fallbackErr := fetchActivityWebFingerHostMetaURL(domain, resource)
		if fallbackErr != nil {
			return "", fallbackErr
		}
		doc, err = fetchActivityWebFingerDocument(fallbackURL)
	}
	if err != nil {
		return "", err
	}
	subjectUsername, subjectDomain, ok := activityWebFingerSubjectUsernameAndDomainRaw(doc.Subject)
	if !ok {
		return "", fmt.Errorf("webfinger subject does not match")
	}
	if strings.EqualFold(username, subjectUsername) && strings.EqualFold(domain, subjectDomain) {
		return activityWebFingerSelfLinkHref(doc)
	}
	if depth == 0 {
		return fetchRemoteActorServiceWebFingerURLDepth(subjectUsername, subjectDomain, depth+1)
	}
	return "", fmt.Errorf("webfinger subject does not match")
}

func activityWebFingerSubjectUsernameAndDomainRaw(subject string) (string, string, bool) {
	username, domain, ok := strings.Cut(strings.TrimPrefix(subject, "acct:"), "@")
	if !ok || username == "" || domain == "" {
		return "", "", false
	}
	return username, domain, true
}

func activityWebFingerSelfLinkHref(doc activityWebFinger) (string, error) {
	for _, link := range doc.Links {
		if link.Rel == "self" && activityWebFingerSelfLinkActivityPubReady(link.Type) && link.Href != "" {
			return link.Href, nil
		}
	}
	return "", fmt.Errorf("public key not found")
}

func (s *Server) remoteActivityDomainNotAllowed(raw string) (bool, error) {
	raw = activityPubFetchExpectedID(raw)
	host := activityPubNormalizedURIHost(raw)
	if host == "" {
		return false, nil
	}
	return s.domainNotAllowed(host)
}

func remoteActivityActorPossiblyStale(account models.Account, now time.Time) bool {
	return !account.LastWebfingeredAt.Valid || !account.LastWebfingeredAt.Time.After(now.Add(-24*time.Hour))
}

func activityAttributionTrusted(objectURI string, actorURI string) bool {
	return activityPubNormalizedURIHostsMatch(objectURI, actorURI)
}

func (s *Server) localActivityURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return false
	}
	return s.localActivityHost(parsed.Host)
}
