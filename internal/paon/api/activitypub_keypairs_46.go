package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const mastodon46MaxRemotePublicKeys = 10

type activityRemotePublicKeyDocumentFetcher func(string) (map[string]any, error)

type activityPubResolvedKeypair struct {
	Account models.Account
	Keypair models.Keypair
	Legacy  bool
}

func activityRemoteActorPublicKeys(actorID string, value any, fetcher activityRemotePublicKeyDocumentFetcher) []remoteActivityPublicKey {
	items := activityJSONLDListItems(value)
	if len(items) > mastodon46MaxRemotePublicKeys {
		items = items[:mastodon46MaxRemotePublicKeys]
	}
	out := make([]remoteActivityPublicKey, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		keyID := activityJSONLDValueOrID(item)
		if keyID == "" {
			continue
		}
		if object, ok := activityJSONLDSingle(item).(map[string]any); ok {
			inline := activityRemotePublicKeyFromObject(object)
			if inline.Owner != actorID {
				continue
			}
			// Mastodon trusts an inline key only when it is a fragment of the
			// actor. Other IDs are dereferenced even if the actor included PEM.
			if strings.Split(keyID, "#")[0] == actorID && inline.PublicKeyPem != "" {
				inline.ID = keyID
				if _, duplicate := seen[keyID]; !duplicate {
					seen[keyID] = struct{}{}
					out = append(out, inline)
				}
				continue
			}
		}
		if fetcher == nil {
			continue
		}
		document, err := fetcher(keyID)
		if err != nil || document == nil {
			// An unavailable unrelated key does not prevent processing later
			// keys. A request signed by that unavailable key still fails closed.
			continue
		}
		key := activityRemotePublicKeyFromDocument(document, keyID)
		if key.Owner != actorID || key.PublicKeyPem == "" {
			continue
		}
		key.ID = keyID
		if _, duplicate := seen[keyID]; duplicate {
			continue
		}
		seen[keyID] = struct{}{}
		out = append(out, key)
	}
	return out
}

func activityRemotePublicKeyFromDocument(document map[string]any, keyID string) remoteActivityPublicKey {
	for _, item := range activityJSONLDGraphMaps(document) {
		if activityJSONLDID(item) == keyID && activityJSONLDString(item, "publicKeyPem") != "" {
			return activityRemotePublicKeyFromObject(item)
		}
	}
	if graphKey := activityJSONLDGraphPublicKey(document); graphKey != nil {
		document = graphKey
	}
	if publicKeyValue := activityJSONLDValue(document, "publicKey"); publicKeyValue != nil {
		// GoToSocial historically returned the whole actor at a key URL. Select
		// the requested key, not merely the first key in the actor document.
		for _, item := range activityJSONLDListItems(publicKeyValue) {
			if activityJSONLDValueOrID(item) != keyID {
				continue
			}
			if object, ok := activityJSONLDSingle(item).(map[string]any); ok {
				return activityRemotePublicKeyFromObject(object)
			}
		}
		return remoteActivityPublicKey{}
	}
	key := activityRemotePublicKeyFromObject(document)
	if key.ID != "" && key.ID != keyID {
		return remoteActivityPublicKey{}
	}
	return key
}

func activityRemotePublicKeyFromObject(object map[string]any) remoteActivityPublicKey {
	key := remoteActivityPublicKey{
		ID:           activityJSONLDID(object),
		IDRaw:        activityJSONLDValueOrID(activityJSONLDValue(object, "id")),
		Owner:        activityJSONLDObjectID(object, "owner"),
		OwnerRaw:     activityJSONLDValueOrID(activityJSONLDValue(object, "owner")),
		PublicKeyPem: activityJSONLDString(object, "publicKeyPem"),
		Revoked:      activityBoolValue(activityJSONLDValue(object, "revoked")),
	}
	for _, field := range []string{"expires", "expiresAt"} {
		raw := activityJSONLDString(object, field)
		if raw == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			key.ExpiresAt = sql.NullTime{Time: parsed.UTC(), Valid: true}
		}
		break
	}
	return key
}

func activityRemoteActorHasPublicKey(actor remoteActivityActor) bool {
	for _, key := range actor.PublicKeys {
		if strings.TrimSpace(key.ID) != "" && strings.TrimSpace(key.PublicKeyPem) != "" {
			return true
		}
	}
	return strings.TrimSpace(actor.PublicKey.PublicKeyPem) != ""
}

func activityRemoteActorContainsPublicKeyID(actor remoteActivityActor, keyID string) bool {
	for _, key := range actor.PublicKeys {
		if activityRawNonBlank(key.IDRaw, key.ID) == keyID {
			return true
		}
	}
	for _, item := range activityJSONLDListItems(actor.PublicKeySource) {
		if activityJSONLDValueOrID(item) == keyID {
			return true
		}
	}
	return activityRemoteActorPublicKeyRawID(actor.PublicKey) == keyID
}

func setActivityRemoteActorPublicKeys(actor *remoteActivityActor, keys []remoteActivityPublicKey) {
	if actor == nil {
		return
	}
	actor.PublicKeys = append(actor.PublicKeys[:0], keys...)
	actor.PublicKey = remoteActivityPublicKey{}
	if len(actor.PublicKeys) > 0 {
		actor.PublicKey = actor.PublicKeys[0]
	}
}

func (s *Server) fetchRemoteActivityPublicKeyDocument(uri string) (map[string]any, error) {
	resource, err := s.fetchActivityResourceWithRepresentative(uri)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(resource.body, &document); err != nil {
		return nil, err
	}
	if contextValue, present := document["@context"]; present && !activityResourceSupportedContext(contextValue) && !activityRemotePublicKeySecurityContext(document, contextValue) {
		return nil, fmt.Errorf("unsupported activity context")
	}
	return document, nil
}

func fetchRemoteActivityPublicKeyDocument(uri string) (map[string]any, error) {
	resource, err := fetchActivityResourceWithMetadata(uri)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if err := json.Unmarshal(resource.body, &document); err != nil {
		return nil, err
	}
	if contextValue, present := document["@context"]; present && !activityResourceSupportedContext(contextValue) && !activityRemotePublicKeySecurityContext(document, contextValue) {
		return nil, fmt.Errorf("unsupported activity context")
	}
	return document, nil
}

func activityRemotePublicKeySecurityContext(document map[string]any, contextValue any) bool {
	keyObject := document
	if graphKey := activityJSONLDGraphPublicKey(document); graphKey != nil {
		keyObject = graphKey
	}
	if activityJSONLDString(keyObject, "publicKeyPem") == "" || activityJSONLDValueOrID(activityJSONLDValue(keyObject, "owner")) == "" {
		return false
	}
	for _, item := range activityJSONLDListItems(contextValue) {
		value, _ := item.(string)
		switch value {
		case "https://w3id.org/security/v1", "https://w3id.org/identity/v1":
			return true
		}
	}
	return false
}

func remoteActivityKeypairRows(keys []remoteActivityPublicKey, accountID int64, now time.Time) []models.Keypair {
	rows := make([]models.Keypair, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		uri := strings.TrimSpace(key.ID)
		publicKey := strings.TrimSpace(key.PublicKeyPem)
		if uri == "" || publicKey == "" {
			continue
		}
		if _, duplicate := seen[uri]; duplicate {
			continue
		}
		seen[uri] = struct{}{}
		rows = append(rows, models.Keypair{
			AccountID:  accountID,
			URI:        uri,
			Type:       0,
			PublicKey:  key.PublicKeyPem,
			PrivateKey: sql.NullString{},
			ExpiresAt:  key.ExpiresAt,
			Revoked:    key.Revoked,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	}
	return rows
}

func syncRemoteActivityActorKeypairs(tx *gorm.DB, accountID int64, keys []remoteActivityPublicKey, now time.Time) ([]models.Keypair, error) {
	if tx == nil || accountID == 0 {
		return nil, nil
	}
	rows := remoteActivityKeypairRows(keys, accountID, now)
	uris := make([]string, 0, len(rows))
	for index := range rows {
		row := rows[index]
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "uri"}},
			DoUpdates: clause.Assignments(map[string]any{
				"account_id": row.AccountID,
				"type":       row.Type,
				"public_key": row.PublicKey,
				"expires_at": row.ExpiresAt,
				"revoked":    row.Revoked,
				"updated_at": now,
			}),
		}).Create(&row).Error; err != nil {
			return nil, fmt.Errorf("upsert remote ActivityPub key %s: %w", row.URI, err)
		}
		uris = append(uris, row.URI)
	}
	deleteQuery := tx.Where("account_id = ?", accountID)
	if len(uris) > 0 {
		deleteQuery = deleteQuery.Where("uri NOT IN ?", uris)
	}
	if err := deleteQuery.Delete(&models.Keypair{}).Error; err != nil {
		return nil, fmt.Errorf("remove obsolete remote ActivityPub keys: %w", err)
	}
	var persisted []models.Keypair
	if err := tx.Where("account_id = ?", accountID).Order("id ASC").Find(&persisted).Error; err != nil {
		return nil, err
	}
	return persisted, nil
}

func activityPubAccountKeyMaterials(tx *gorm.DB, accountID int64, legacyPublicKey string) ([]string, error) {
	materials := make([]string, 0, 4)
	if strings.TrimSpace(legacyPublicKey) != "" {
		materials = append(materials, legacyPublicKey)
	}
	if tx == nil || accountID == 0 {
		return materials, nil
	}
	var stored []string
	if err := tx.Model(&models.Keypair{}).Where("account_id = ?", accountID).Pluck("public_key", &stored).Error; err != nil {
		return nil, err
	}
	return append(materials, stored...), nil
}

func activityPubKeypairUsableAt(keypair models.Keypair, now time.Time) bool {
	if keypair.Revoked {
		return false
	}
	return !keypair.ExpiresAt.Valid || keypair.ExpiresAt.Time.After(now)
}

func activityPubAllPublicKeysChanged(oldMaterials []string, current []models.Keypair, now time.Time) bool {
	if len(oldMaterials) == 0 {
		return false
	}
	old := make(map[string]struct{}, len(oldMaterials))
	for _, material := range oldMaterials {
		if strings.TrimSpace(material) != "" {
			old[material] = struct{}{}
		}
	}
	if len(old) == 0 {
		return false
	}
	for _, keypair := range current {
		if !activityPubKeypairUsableAt(keypair, now) {
			continue
		}
		if _, retained := old[keypair.PublicKey]; retained {
			return false
		}
	}
	return true
}

func activityPubLegacyResolvedKeypair(account models.Account, keyID string) *activityPubResolvedKeypair {
	if strings.TrimSpace(account.PublicKey) == "" {
		return nil
	}
	return &activityPubResolvedKeypair{
		Account: account,
		Keypair: models.Keypair{
			AccountID:  account.ID,
			URI:        strings.TrimSpace(keyID),
			Type:       0,
			PublicKey:  account.PublicKey,
			PrivateKey: account.PrivateKey,
		},
		Legacy: true,
	}
}

func (s *Server) activityPubStoredKeypairForKeyID(keyID string) (*activityPubResolvedKeypair, error) {
	if s == nil || s.db == nil || strings.TrimSpace(keyID) == "" {
		return nil, nil
	}
	var keypair models.Keypair
	err := s.db.Where("uri = ?", keyID).First(&keypair).Error
	if err == nil {
		var account models.Account
		if err := s.db.Preload("AccountStat").Where("id = ?", keypair.AccountID).First(&account).Error; err != nil {
			return nil, err
		}
		return &activityPubResolvedKeypair{Account: account, Keypair: keypair}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	actorURI := keyID
	if before, _, ok := strings.Cut(actorURI, "#"); ok {
		actorURI = before
	}
	var account models.Account
	err = s.db.Preload("AccountStat").Where("uri = ?", actorURI).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return activityPubLegacyResolvedKeypair(account, keyID), nil
}

func (s *Server) activityPubKeypairFromKeyID(keyID string) (*activityPubResolvedKeypair, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("database is not available")
	}
	keyID = strings.TrimSpace(keyID)
	if disallowed, err := s.activityPubKeyIDDomainNotAllowed(keyID); err != nil || disallowed {
		if err != nil {
			return nil, err
		}
		return nil, errActivitySignatureDomainNotAllowed
	}
	if strings.HasPrefix(keyID, "acct:") {
		username, domain, ok := activityPubAcctKeyIDUsernameDomain(keyID)
		if !ok {
			return nil, fmt.Errorf("public key not found")
		}
		var account models.Account
		query := s.db.Preload("AccountStat")
		localAcct := activityHostWithNormalizedName(domain) == activityHostWithNormalizedName(s.cfg.LocalDomain)
		if localAcct {
			query = query.Where("lower(username) = lower(?) AND domain IS NULL", username)
		} else {
			query = query.Where("lower(username) = lower(?) AND lower(domain) = lower(?)", username, domain)
		}
		err := query.First(&account).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if localAcct {
				return nil, fmt.Errorf("public key not found")
			}
			accountPointer, fetchErr := s.fetchAndStoreActivityActorForAcct(username + "@" + domain)
			if fetchErr != nil || accountPointer == nil {
				return nil, fetchErr
			}
			account = *accountPointer
		} else if err != nil {
			return nil, err
		}
		var keypair models.Keypair
		err = s.db.Where("account_id = ?", account.ID).Order("id ASC").First(&keypair).Error
		if err == nil {
			return &activityPubResolvedKeypair{Account: account, Keypair: keypair}, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		legacyURI := strings.TrimSuffix(account.URI, "#") + "#main-key"
		return activityPubLegacyResolvedKeypair(account, legacyURI), nil
	}
	if s.localActivityURI(keyID) {
		return nil, fmt.Errorf("public key not found")
	}
	if stored, err := s.activityPubStoredKeypairForKeyID(keyID); err != nil || stored != nil {
		return stored, err
	}
	if !strings.HasPrefix(keyID, "http://") && !strings.HasPrefix(keyID, "https://") {
		return nil, fmt.Errorf("public key not found")
	}
	account, err := s.fetchAndStoreActivityActorForKeyID(keyID)
	if err != nil || account == nil {
		return nil, err
	}
	var keypair models.Keypair
	err = s.db.Where("uri = ? AND account_id = ?", keyID, account.ID).First(&keypair).Error
	if err == nil {
		return &activityPubResolvedKeypair{Account: *account, Keypair: keypair}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if legacy := activityPubLegacyResolvedKeypair(*account, keyID); legacy != nil {
		return legacy, nil
	}
	return nil, fmt.Errorf("public key not found")
}

func (s *Server) activityPubKeypairFromKeyIDWithSourceStoplight(c *echo.Context, keyID string) (*activityPubResolvedKeypair, error) {
	return activityPubSignatureSourceStoplightWrapKeypair(s, c, func() (*activityPubResolvedKeypair, error) {
		return s.activityPubKeypairFromKeyID(keyID)
	})
}

func activityPubSignatureSourceStoplightWrapKeypair(s *Server, c *echo.Context, fn func() (*activityPubResolvedKeypair, error)) (*activityPubResolvedKeypair, error) {
	source := activityPubSignatureSourceIP(c)
	if s.activityPubSignatureSourceStoplightOpen(source) {
		return nil, fmt.Errorf("fetching attempt skipped because of recent connection failure")
	}
	keypair, err := fn()
	if err != nil {
		s.trackActivityPubSignatureSourceStoplightFailure(source)
		return keypair, err
	}
	s.trackActivityPubSignatureSourceStoplightSuccess(source)
	return keypair, nil
}

func (s *Server) refreshActivityPubResolvedKeypair(keyID string, resolved *activityPubResolvedKeypair) (*activityPubResolvedKeypair, error) {
	if resolved == nil || resolved.Account.Local() {
		return resolved, nil
	}
	account, err := s.refreshKnownActivityPubActorOnlyKey(&resolved.Account)
	if err != nil || account == nil {
		return nil, err
	}
	lookupURI := strings.TrimSpace(resolved.Keypair.URI)
	if lookupURI == "" {
		lookupURI = keyID
	}
	var keypair models.Keypair
	err = s.db.Where("uri = ? AND account_id = ?", lookupURI, account.ID).First(&keypair).Error
	if err == nil {
		return &activityPubResolvedKeypair{Account: *account, Keypair: keypair}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return activityPubLegacyResolvedKeypair(*account, lookupURI), nil
}

func (s *Server) refreshActivityPubResolvedKeypairWithSourceStoplight(c *echo.Context, keyID string, resolved *activityPubResolvedKeypair) (*activityPubResolvedKeypair, error) {
	if resolved == nil || resolved.Account.Local() {
		return s.refreshActivityPubResolvedKeypair(keyID, resolved)
	}
	return activityPubSignatureSourceStoplightWrapKeypair(s, c, func() (*activityPubResolvedKeypair, error) {
		return s.refreshActivityPubResolvedKeypair(keyID, resolved)
	})
}

func activityPubResolvedKeypairValidityError(keyID string, resolved *activityPubResolvedKeypair, now time.Time) error {
	if resolved == nil {
		return fmt.Errorf("public key not found for key %s", keyID)
	}
	if resolved.Keypair.Revoked {
		return fmt.Errorf("key %s is revoked", keyID)
	}
	if resolved.Keypair.ExpiresAt.Valid && !resolved.Keypair.ExpiresAt.Time.After(now) {
		return fmt.Errorf("key %s has expired", keyID)
	}
	return nil
}
