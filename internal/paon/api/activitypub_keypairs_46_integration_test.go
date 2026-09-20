//go:build integration

package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	paondb "github.com/mstdn-plusminus-io/paon/internal/paon/db"
	"github.com/mstdn-plusminus-io/paon/internal/paon/migrate"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
	"gorm.io/gorm"
)

func TestMastodon46RemoteKeypairPersistenceMergeFallbackAndDeleteAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("PAON_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("PAON_TEST_DATABASE_URL is required for integration tests")
	}
	database, err := paondb.Open(config.Config{DatabaseURL: databaseURL, DatabaseMaxOpenConns: 3, DatabaseMaxIdleConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if _, err := migrate.Run(t.Context(), database); err != nil {
		t.Fatal(err)
	}
	errRollback := errors.New("rollback keypair integration fixture")
	err = database.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC().Truncate(time.Microsecond)
		canonicalID := insertMastodon46RemoteKeypairTestAccount(t, tx, "canonical", "https://remote.example/users/canonical", "")
		duplicateID := insertMastodon46RemoteKeypairTestAccount(t, tx, "duplicate", "https://remote.example/users/duplicate", "")
		legacyID := insertMastodon46RemoteKeypairTestAccount(t, tx, "legacy", "https://remote.example/users/legacy", "LEGACY")

		keys, err := syncRemoteActivityActorKeypairs(tx, duplicateID, []remoteActivityPublicKey{
			{ID: "https://remote.example/users/canonical#one", Owner: "https://remote.example/users/canonical", PublicKeyPem: "ONE"},
			{ID: "https://remote.example/users/canonical#obsolete", Owner: "https://remote.example/users/canonical", PublicKeyPem: "OBSOLETE"},
		}, now)
		if err != nil || len(keys) != 2 {
			t.Fatalf("initial sync = %#v, %v", keys, err)
		}
		keys, err = syncRemoteActivityActorKeypairs(tx, canonicalID, []remoteActivityPublicKey{
			{ID: "https://remote.example/users/canonical#one", Owner: "https://remote.example/users/canonical", PublicKeyPem: "ONE-ROTATED"},
			{ID: "https://remote.example/users/canonical#two", Owner: "https://remote.example/users/canonical", PublicKeyPem: "TWO", Revoked: true},
		}, now.Add(time.Second))
		if err != nil || len(keys) != 2 {
			t.Fatalf("canonical sync = %#v, %v", keys, err)
		}
		var reassigned models.Keypair
		if err := tx.Where("uri = ?", "https://remote.example/users/canonical#one").First(&reassigned).Error; err != nil || reassigned.AccountID != canonicalID || reassigned.PublicKey != "ONE-ROTATED" {
			t.Fatalf("reassigned key = %#v, %v", reassigned, err)
		}
		if err := tx.Where("id = ?", duplicateID).Delete(&models.Account{}).Error; err != nil {
			t.Fatal(err)
		}
		var retained int64
		if err := tx.Model(&models.Keypair{}).Where("account_id = ?", canonicalID).Count(&retained).Error; err != nil || retained != 2 {
			t.Fatalf("canonical key count after duplicate delete = %d, %v", retained, err)
		}

		server := &Server{db: tx, cfg: config.Config{LocalDomain: "local.example"}}
		resolved, err := server.activityPubStoredKeypairForKeyID("https://remote.example/users/canonical#two")
		if err != nil || resolved == nil || resolved.Account.ID != canonicalID || !resolved.Keypair.Revoked {
			t.Fatalf("stored key resolution = %#v, %v", resolved, err)
		}
		if err := activityPubResolvedKeypairValidityError(resolved.Keypair.URI, resolved, now); err == nil {
			t.Fatal("revoked stored key passed validity check")
		}

		secondaryPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		secondaryPublicDER, err := x509.MarshalPKIXPublicKey(&secondaryPrivateKey.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		secondaryPublicPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: secondaryPublicDER}))
		if err := tx.Model(&models.Keypair{}).Where("uri = ?", "https://remote.example/users/canonical#one").Updates(map[string]any{"public_key": secondaryPublicPEM, "revoked": false, "expires_at": nil}).Error; err != nil {
			t.Fatal(err)
		}
		echoContext := mastodon46SignedDraftRequestContext(t, secondaryPrivateKey, "https://remote.example/users/canonical#one")
		verified, err := server.verifyActivityPubSignature(echoContext, nil)
		if err != nil || verified == nil || verified.ID != canonicalID {
			t.Fatalf("secondary key HTTP Signature verification = %#v, %v", verified, err)
		}
		// The backported LD diagnostics must verify the selected 4.6 keypair,
		// even though the account itself has no legacy public key.
		linkedDocument := map[string]any{
			"@context": []string{"https://www.w3.org/ns/activitystreams", "https://w3id.org/security/v1"},
			"type":     "Delete", "actor": "https://remote.example/users/canonical",
			"object": "https://remote.example/statuses/keypair-test",
			"signature": map[string]any{
				"type": "RsaSignature2017", "creator": "https://remote.example/users/canonical#one",
				"created": now.Format(time.RFC3339), "signatureValue": "pending",
			},
		}
		_, toVerify, err := activityPubLinkedDataSignatureVerificationString(linkedDocument)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(toVerify))
		signature, err := rsa.SignPKCS1v15(rand.Reader, secondaryPrivateKey, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		linkedDocument["signature"].(map[string]any)["signatureValue"] = base64.StdEncoding.EncodeToString(signature)
		linkedBody, err := json.Marshal(linkedDocument)
		if err != nil {
			t.Fatal(err)
		}
		linkedPayload, err := parseActivityPayload(linkedBody)
		if err != nil {
			t.Fatal(err)
		}
		if actor, err := server.activityPubLinkedDataSignatureActor(linkedBody, linkedPayload); err != nil || actor == nil || actor.ID != canonicalID {
			t.Fatalf("secondary key Linked Data signature = %#v, %v", actor, err)
		}
		linkedDocument["object"] = "https://remote.example/statuses/tampered"
		tamperedBody, err := json.Marshal(linkedDocument)
		if err != nil {
			t.Fatal(err)
		}
		_, linkedErr := server.activityPubLinkedDataSignatureActor(tamperedBody, linkedPayload)
		var diagnostic *activityPubSignatureVerificationError
		if !errors.As(linkedErr, &diagnostic) || diagnostic.Diagnostics.SignatureActorID != canonicalID {
			t.Fatalf("secondary key Linked Data signature diagnostic = %v", linkedErr)
		}
		if err := tx.Model(&models.Keypair{}).Where("uri = ?", "https://remote.example/users/canonical#one").Update("revoked", true).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := server.verifyActivityPubSignature(echoContext, nil); err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("revoked secondary HTTP Signature error = %v", err)
		}
		if _, err := server.activityPubLinkedDataSignatureActor(linkedBody, linkedPayload); err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("revoked secondary Linked Data signature error = %v", err)
		}
		if err := tx.Model(&models.Keypair{}).Where("uri = ?", "https://remote.example/users/canonical#one").Updates(map[string]any{"revoked": false, "expires_at": now.Add(-time.Minute)}).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := server.activityPubLinkedDataSignatureActor(linkedBody, linkedPayload); err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("expired secondary Linked Data signature error = %v", err)
		}
		legacy, err := server.activityPubStoredKeypairForKeyID("https://remote.example/users/legacy#main-key")
		if err != nil || legacy == nil || !legacy.Legacy || legacy.Account.ID != legacyID || legacy.Keypair.PublicKey != "LEGACY" {
			t.Fatalf("legacy fallback = %#v, %v", legacy, err)
		}
		acctLegacy, err := server.activityPubKeypairFromKeyID("acct:legacy@remote.example")
		if err != nil || acctLegacy == nil || acctLegacy.Keypair.URI != "https://remote.example/users/legacy#main-key" {
			t.Fatalf("acct legacy fallback = %#v, %v", acctLegacy, err)
		}

		if err := tx.Where("id = ?", canonicalID).Delete(&models.Account{}).Error; err != nil {
			t.Fatal(err)
		}
		if err := tx.Model(&models.Keypair{}).Where("account_id = ?", canonicalID).Count(&retained).Error; err != nil || retained != 0 {
			t.Fatalf("key count after account delete = %d, %v", retained, err)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("fixture rollback = %v", err)
	}
}

func mastodon46SignedDraftRequestContext(t *testing.T, privateKey *rsa.PrivateKey, keyID string) *echo.Context {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "https://local.example/activitypub/key-test", nil)
	request.Host = "local.example"
	date := time.Now().UTC().Format(http.TimeFormat)
	request.Header.Set("Date", date)
	signed := "date: " + date + "\n" +
		"host: local.example\n" +
		"(request-target): get /activitypub/key-test"
	digest := sha256.Sum256([]byte(signed))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Signature", fmt.Sprintf(`keyId=%q,algorithm="rsa-sha256",headers="date host (request-target)",signature=%q`, keyID, base64.StdEncoding.EncodeToString(signature)))
	context := echo.NewContext(request, httptest.NewRecorder(), echo.New())
	return context
}

func insertMastodon46RemoteKeypairTestAccount(t *testing.T, tx *gorm.DB, username, uri, publicKey string) int64 {
	t.Helper()
	var id int64
	if err := tx.Raw(`INSERT INTO accounts (username, domain, public_key, created_at, updated_at, uri) VALUES (?, 'remote.example', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?) RETURNING id`, username, publicKey, uri).Scan(&id).Error; err != nil {
		t.Fatal(err)
	}
	return id
}
