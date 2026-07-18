package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/mstdn-plusminus-io/paon/internal/paon/config"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	webauthnChallengeCookie       = "paon_webauthn_challenge"
	webauthnCreateChallengeCookie = "paon_webauthn_create_challenge"
	webauthnAttemptUserCookie     = "paon_webauthn_attempt_user_id"
	webauthnAttemptRedirectCookie = "paon_webauthn_attempt_redirect"
	railsWebauthnRPName           = "Mastodon"
	railsWebauthnTimeout          = 120000
)

type paonWebAuthnUser struct {
	user        *models.User
	account     *models.Account
	credentials []models.WebauthnCredential
}

func (u paonWebAuthnUser) WebAuthnID() []byte {
	if u.user != nil && u.user.WebauthnID.Valid {
		return []byte(u.user.WebauthnID.String)
	}
	return nil
}

func (u paonWebAuthnUser) WebAuthnName() string {
	if u.account != nil {
		return u.account.Username
	}
	if u.user != nil {
		return u.user.Email
	}
	return ""
}

func (u paonWebAuthnUser) WebAuthnDisplayName() string {
	return u.WebAuthnName()
}

func (u paonWebAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(u.credentials))
	for _, row := range u.credentials {
		credential, ok := webauthnCredentialFromModel(row)
		if ok {
			out = append(out, credential)
		}
	}
	return out
}

func (s *Server) webauthnProvider() (*webauthn.WebAuthn, error) {
	rpID := webauthnRPID(s.cfg.WebDomain)
	return webauthn.New(&webauthn.Config{
		RPDisplayName: railsWebauthnRPName,
		RPID:          rpID,
		RPOrigins:     []string{webauthnRPOrigin(s.cfg)},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationDiscouraged,
		},
		AttestationPreference: protocol.PreferNoAttestation,
		EncodeUserIDAsString:  true,
	})
}

func webauthnRPOrigin(cfg config.Config) string {
	return cfg.BaseURL()
}

func (s *Server) webauthnUser(user *models.User) (paonWebAuthnUser, error) {
	account, err := s.userAccount(user.AccountID)
	if err != nil {
		return paonWebAuthnUser{}, err
	}
	credentials, err := s.webauthnCredentialsForUser(user.ID)
	if err != nil {
		return paonWebAuthnUser{}, err
	}
	return paonWebAuthnUser{user: user, account: account, credentials: credentials}, nil
}

func webauthnCredentialFromModel(row models.WebauthnCredential) (webauthn.Credential, bool) {
	id, ok := decodeWebauthnStoredBytes(row.ExternalID)
	if !ok {
		return webauthn.Credential{}, false
	}
	publicKey, ok := decodeWebauthnStoredBytes(row.PublicKey)
	if !ok {
		return webauthn.Credential{}, false
	}
	return webauthn.Credential{
		ID:        id,
		PublicKey: publicKey,
		Authenticator: webauthn.Authenticator{
			SignCount: uint32(row.SignCount),
		},
	}, true
}

func webauthnCredentialExternalID(credential *webauthn.Credential) string {
	if credential == nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(credential.ID)
}

func webauthnCredentialPublicKey(credential *webauthn.Credential) string {
	if credential == nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(credential.PublicKey)
}

func decodeWebauthnStoredBytes(value string) ([]byte, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, false
	}
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	for _, encoding := range encodings {
		if decoded, err := encoding.DecodeString(value); err == nil && len(decoded) > 0 {
			return decoded, true
		}
	}
	return []byte(value), true
}

func webauthnRegistrationSession(user *models.User, challenge string, rpID string) webauthn.SessionData {
	return webauthn.SessionData{
		Challenge:        challenge,
		RelyingPartyID:   rpID,
		UserID:           []byte(user.WebauthnID.String),
		UserVerification: protocol.VerificationDiscouraged,
		CredParams:       webauthn.CredentialParametersDefault(),
	}
}

func webauthnLoginSession(user *models.User, challenge string, rpID string, credentials []models.WebauthnCredential) webauthn.SessionData {
	allowed := make([][]byte, 0, len(credentials))
	for _, credential := range credentials {
		if id, ok := decodeWebauthnStoredBytes(credential.ExternalID); ok {
			allowed = append(allowed, id)
		}
	}
	return webauthn.SessionData{
		Challenge:            challenge,
		RelyingPartyID:       rpID,
		UserID:               []byte(user.WebauthnID.String),
		AllowedCredentialIDs: allowed,
		UserVerification:     protocol.VerificationDiscouraged,
	}
}

func webauthnRPID(webDomain string) string {
	host := strings.TrimSpace(webDomain)
	if host == "" {
		return "localhost"
	}
	if parsed, err := url.Parse(host); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	return strings.Trim(host, "[]")
}

func webauthnChallenge() string {
	challenge, err := protocol.CreateChallenge()
	if err != nil {
		return randomHex(32)
	}
	return challenge.String()
}

type webauthnCredentialEnvelope struct {
	Credential json.RawMessage `json:"credential"`
	Nickname   string          `json:"nickname"`
	Name       string          `json:"name"`
	User       struct {
		Credential json.RawMessage `json:"credential"`
		Nickname   string          `json:"nickname"`
		Name       string          `json:"name"`
	} `json:"user"`
}

func webauthnCredentialRequest(request *http.Request) (*http.Request, webauthnCredentialEnvelope, error) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, webauthnCredentialEnvelope{}, err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))

	var envelope webauthnCredentialEnvelope
	credentialBody := bytes.TrimSpace(body)
	if err := json.Unmarshal(body, &envelope); err == nil {
		switch {
		case len(bytes.TrimSpace(envelope.Credential)) > 0:
			credentialBody = bytes.TrimSpace(envelope.Credential)
		case len(bytes.TrimSpace(envelope.User.Credential)) > 0:
			credentialBody = bytes.TrimSpace(envelope.User.Credential)
		}
	}

	credentialRequest := request.Clone(request.Context())
	credentialRequest.Body = io.NopCloser(bytes.NewReader(credentialBody))
	credentialRequest.ContentLength = int64(len(credentialBody))
	credentialRequest.Header = request.Header.Clone()
	credentialRequest.Header.Set("Content-Type", "application/json")
	return credentialRequest, envelope, nil
}

func requestContainsWebauthnCredential(request *http.Request) bool {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return false
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	var envelope webauthnCredentialEnvelope
	return json.Unmarshal(body, &envelope) == nil && (len(bytes.TrimSpace(envelope.Credential)) > 0 || len(bytes.TrimSpace(envelope.User.Credential)) > 0)
}
