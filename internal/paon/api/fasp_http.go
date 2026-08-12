package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

const (
	faspFeatureName       = "fasp"
	faspSignatureLabel    = "sig1"
	faspRequestBodyLimit  = int64(1 << 20)
	faspResponseBodyLimit = int64(2 << 20)
	faspSignatureMaxAge   = 5 * time.Minute
	faspHTTPTimeout       = 15 * time.Second
)

var (
	errFASPDisabled            = errors.New("fasp is disabled")
	errFASPAuthentication      = errors.New("fasp authentication failed")
	errFASPUnconfirmedProvider = errors.New("fasp provider is not confirmed")
)

type faspCapability struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
}

func (s *Server) faspEnabled() bool {
	return s != nil && s.cfg.ExperimentalFeatureEnabled(faspFeatureName)
}

func faspGenerateServerKey() (string, string, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", "", err
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	return privatePEM, base64.StdEncoding.EncodeToString(publicKey), nil
}

func faspPublicKeyPEMFromBase64(raw string) (string, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(raw))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return "", fmt.Errorf("invalid Ed25519 public key")
	}
	der, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(decoded))
	if err != nil {
		return "", fmt.Errorf("invalid Ed25519 public key")
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}

func faspParsePrivateKey(raw string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, fmt.Errorf("invalid Ed25519 private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid Ed25519 private key")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid Ed25519 private key")
	}
	return key, nil
}

func faspParsePublicKey(raw string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, fmt.Errorf("invalid Ed25519 public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("invalid Ed25519 public key")
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid Ed25519 public key")
	}
	return key, nil
}

func faspServerPublicKeyBase64(privatePEM string) (string, error) {
	key, err := faspParsePrivateKey(privatePEM)
	if err != nil {
		return "", err
	}
	publicKey, ok := key.Public().(ed25519.PublicKey)
	if !ok {
		return "", fmt.Errorf("invalid Ed25519 private key")
	}
	return base64.StdEncoding.EncodeToString(publicKey), nil
}

func faspProviderPublicKeyFingerprint(provider models.FaspProvider) (string, error) {
	key, err := faspParsePublicKey(provider.ProviderPublicKeyPEM)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(key)
	return base64.StdEncoding.EncodeToString(digest[:]), nil
}

func faspContentDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(digest[:]) + ":"
}

func faspValidateContentDigest(header string, body []byte) error {
	want := faspContentDigest(body)
	if strings.TrimSpace(header) == "" || header != want {
		return errFASPAuthentication
	}
	return nil
}

type faspSignatureInput struct {
	Label      string
	Components []string
	Parameters string
	Created    int64
	KeyID      string
}

func faspParseSignatureInput(raw string) (faspSignatureInput, error) {
	raw = strings.TrimSpace(raw)
	equals := strings.IndexByte(raw, '=')
	if equals <= 0 {
		return faspSignatureInput{}, errFASPAuthentication
	}
	parsed := faspSignatureInput{Label: strings.TrimSpace(raw[:equals]), Parameters: strings.TrimSpace(raw[equals+1:])}
	if parsed.Label == "" || !strings.HasPrefix(parsed.Parameters, "(") {
		return faspSignatureInput{}, errFASPAuthentication
	}
	closeIndex := strings.IndexByte(parsed.Parameters, ')')
	if closeIndex <= 1 {
		return faspSignatureInput{}, errFASPAuthentication
	}
	componentText := parsed.Parameters[1:closeIndex]
	for len(strings.TrimSpace(componentText)) > 0 {
		componentText = strings.TrimSpace(componentText)
		if !strings.HasPrefix(componentText, "\"") {
			return faspSignatureInput{}, errFASPAuthentication
		}
		componentText = componentText[1:]
		end := strings.IndexByte(componentText, '"')
		if end <= 0 {
			return faspSignatureInput{}, errFASPAuthentication
		}
		component := strings.ToLower(componentText[:end])
		parsed.Components = append(parsed.Components, component)
		componentText = componentText[end+1:]
	}
	parameterText := parsed.Parameters[closeIndex+1:]
	for _, item := range strings.Split(parameterText, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		value = strings.Trim(strings.TrimSpace(value), "\"")
		switch name {
		case "created":
			created, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return faspSignatureInput{}, errFASPAuthentication
			}
			parsed.Created = created
		case "keyid":
			parsed.KeyID = value
		}
	}
	if parsed.Created == 0 {
		return faspSignatureInput{}, errFASPAuthentication
	}
	return parsed, nil
}

func faspSignatureBytes(raw string, label string) ([]byte, error) {
	for _, member := range strings.Split(raw, ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(member), "=")
		if !ok || strings.TrimSpace(name) != label {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) < 3 || value[0] != ':' || value[len(value)-1] != ':' {
			return nil, errFASPAuthentication
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(value[1 : len(value)-1])
		if err != nil {
			return nil, errFASPAuthentication
		}
		return decoded, nil
	}
	return nil, errFASPAuthentication
}

func faspHasRequiredComponents(actual []string, required ...string) bool {
	seen := make(map[string]struct{}, len(actual))
	for _, component := range actual {
		seen[strings.ToLower(component)] = struct{}{}
	}
	for _, component := range required {
		if _, ok := seen[component]; !ok {
			return false
		}
	}
	return true
}

func faspSignatureBase(input faspSignatureInput, componentValue func(string) (string, error)) (string, error) {
	lines := make([]string, 0, len(input.Components)+1)
	for _, component := range input.Components {
		value, err := componentValue(component)
		if err != nil {
			return "", err
		}
		lines = append(lines, "\""+component+"\": "+value)
	}
	lines = append(lines, "\"@signature-params\": "+input.Parameters)
	return strings.Join(lines, "\n"), nil
}

func faspSignatureString(value string) (string, error) {
	var escaped strings.Builder
	escaped.Grow(len(value))
	for _, char := range []byte(value) {
		if char < 0x20 || char > 0x7e {
			return "", fmt.Errorf("invalid FASP signature parameter")
		}
		if char == '\\' || char == '"' {
			escaped.WriteByte('\\')
		}
		escaped.WriteByte(char)
	}
	return escaped.String(), nil
}

func faspRequestTargetURI(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	if req.URL.IsAbs() {
		return req.URL.String()
	}
	scheme := "http"
	if req.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(req.Header.Get("X-Forwarded-Proto"), ",")[0]), "https") {
		scheme = "https"
	}
	return scheme + "://" + req.Host + req.URL.RequestURI()
}

func faspVerifyHTTPRequest(req *http.Request, body []byte, publicKey ed25519.PublicKey, now time.Time) (faspSignatureInput, error) {
	if req == nil {
		return faspSignatureInput{}, errFASPAuthentication
	}
	if err := faspValidateContentDigest(req.Header.Get("Content-Digest"), body); err != nil {
		return faspSignatureInput{}, errFASPAuthentication
	}
	input, err := faspParseSignatureInput(req.Header.Get("Signature-Input"))
	if err != nil || input.KeyID == "" || !faspHasRequiredComponents(input.Components, "@method", "@target-uri", "content-digest") {
		return faspSignatureInput{}, errFASPAuthentication
	}
	createdAt := time.Unix(input.Created, 0)
	if createdAt.Before(now.Add(-faspSignatureMaxAge)) || createdAt.After(now.Add(faspSignatureMaxAge)) {
		return faspSignatureInput{}, errFASPAuthentication
	}
	base, err := faspSignatureBase(input, func(component string) (string, error) {
		switch component {
		case "@method":
			return strings.ToUpper(req.Method), nil
		case "@target-uri":
			return faspRequestTargetURI(req), nil
		case "content-digest":
			return req.Header.Get("Content-Digest"), nil
		default:
			return "", errFASPAuthentication
		}
	})
	if err != nil {
		return faspSignatureInput{}, errFASPAuthentication
	}
	signature, err := faspSignatureBytes(req.Header.Get("Signature"), input.Label)
	if err != nil || !ed25519.Verify(publicKey, []byte(base), signature) {
		return faspSignatureInput{}, errFASPAuthentication
	}
	return input, nil
}

func faspSignHTTPRequest(req *http.Request, body []byte, privateKey ed25519.PrivateKey, keyID string, now time.Time) error {
	if req == nil || req.URL == nil || len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("cannot sign FASP request")
	}
	digest := faspContentDigest(body)
	req.Header.Set("Content-Digest", digest)
	params := "(\"@method\" \"@target-uri\" \"content-digest\");created=" + strconv.FormatInt(now.Unix(), 10)
	if keyID != "" {
		escapedKeyID, err := faspSignatureString(keyID)
		if err != nil {
			return err
		}
		params += ";keyid=\"" + escapedKeyID + "\""
	}
	input := faspSignatureInput{Label: faspSignatureLabel, Components: []string{"@method", "@target-uri", "content-digest"}, Parameters: params}
	base, err := faspSignatureBase(input, func(component string) (string, error) {
		switch component {
		case "@method":
			return strings.ToUpper(req.Method), nil
		case "@target-uri":
			return req.URL.String(), nil
		case "content-digest":
			return digest, nil
		default:
			return "", fmt.Errorf("unsupported FASP signature component")
		}
	})
	if err != nil {
		return err
	}
	signature := ed25519.Sign(privateKey, []byte(base))
	req.Header.Set("Signature-Input", faspSignatureLabel+"="+params)
	req.Header.Set("Signature", faspSignatureLabel+"=:"+base64.StdEncoding.EncodeToString(signature)+":")
	return nil
}

func faspSignHTTPResponse(header http.Header, status int, body []byte, privateKey ed25519.PrivateKey, now time.Time) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("cannot sign FASP response")
	}
	digest := faspContentDigest(body)
	header.Set("Content-Digest", digest)
	params := "(\"@status\" \"content-digest\");created=" + strconv.FormatInt(now.Unix(), 10)
	input := faspSignatureInput{Label: faspSignatureLabel, Components: []string{"@status", "content-digest"}, Parameters: params}
	base, err := faspSignatureBase(input, func(component string) (string, error) {
		switch component {
		case "@status":
			return strconv.Itoa(status), nil
		case "content-digest":
			return digest, nil
		default:
			return "", fmt.Errorf("unsupported FASP signature component")
		}
	})
	if err != nil {
		return err
	}
	signature := ed25519.Sign(privateKey, []byte(base))
	header.Set("Signature-Input", faspSignatureLabel+"="+params)
	header.Set("Signature", faspSignatureLabel+"=:"+base64.StdEncoding.EncodeToString(signature)+":")
	return nil
}

func faspVerifyHTTPResponse(response *http.Response, body []byte, publicKey ed25519.PublicKey, now time.Time) error {
	if response == nil || response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("FASP provider returned an unsuccessful response")
	}
	if err := faspValidateContentDigest(response.Header.Get("Content-Digest"), body); err != nil {
		return fmt.Errorf("invalid FASP response digest")
	}
	input, err := faspParseSignatureInput(response.Header.Get("Signature-Input"))
	if err != nil || !faspHasRequiredComponents(input.Components, "@status", "content-digest") {
		return fmt.Errorf("invalid FASP response signature")
	}
	createdAt := time.Unix(input.Created, 0)
	if createdAt.Before(now.Add(-faspSignatureMaxAge)) || createdAt.After(now.Add(faspSignatureMaxAge)) {
		return fmt.Errorf("expired FASP response signature")
	}
	base, err := faspSignatureBase(input, func(component string) (string, error) {
		switch component {
		case "@status":
			return strconv.Itoa(response.StatusCode), nil
		case "content-digest":
			return response.Header.Get("Content-Digest"), nil
		default:
			return "", fmt.Errorf("unsupported FASP response component")
		}
	})
	if err != nil {
		return fmt.Errorf("invalid FASP response signature")
	}
	signature, err := faspSignatureBytes(response.Header.Get("Signature"), input.Label)
	if err != nil || !ed25519.Verify(publicKey, []byte(base), signature) {
		return fmt.Errorf("invalid FASP response signature")
	}
	return nil
}

func faspProviderURL(provider models.FaspProvider, endpoint string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimSpace(provider.BaseURL))
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid FASP provider URL")
	}
	if !activityFetchHostAllowed(base.Hostname()) {
		return nil, fmt.Errorf("FASP provider host is not allowed")
	}
	if !strings.HasPrefix(endpoint, "/") {
		return nil, fmt.Errorf("invalid FASP endpoint")
	}
	result, err := url.Parse(strings.TrimRight(base.String(), "/") + endpoint)
	if err != nil || result.Scheme != "https" || result.Hostname() == "" {
		return nil, fmt.Errorf("invalid FASP endpoint")
	}
	return result, nil
}

func (s *Server) faspRequest(ctx context.Context, provider models.FaspProvider, method string, endpoint string, payload any) ([]byte, error) {
	if !s.faspEnabled() {
		return nil, errFASPDisabled
	}
	if !provider.Confirmed {
		return nil, errFASPUnconfirmedProvider
	}
	requestURL, err := faspProviderURL(provider, endpoint)
	if err != nil {
		return nil, err
	}
	body := []byte{}
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	privateKey, err := faspParsePrivateKey(provider.ServerPrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	publicKey, err := faspParsePublicKey(provider.ProviderPublicKeyPEM)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if err := faspSignHTTPRequest(req, body, privateKey, provider.RemoteIdentifier, time.Now().UTC()); err != nil {
		return nil, err
	}
	client := *activityHTTPClientClone(faspHTTPTimeout)
	client.CheckRedirect = func(redirect *http.Request, via []*http.Request) error {
		if redirect == nil || redirect.URL == nil || !strings.EqualFold(redirect.URL.Scheme, "https") || redirect.URL.User != nil {
			return fmt.Errorf("FASP provider redirect is not allowed")
		}
		if err := activityHTTPCheckRedirect(redirect, via); err != nil {
			return err
		}
		// net/http rewrites POST to GET for 301/302/303 responses and drops the
		// body. Sign the request that is actually going to be sent, while 307/308
		// retain GetBody and are re-signed with the original payload.
		redirectBody := []byte{}
		if redirect.GetBody != nil {
			reader, readErr := redirect.GetBody()
			if readErr != nil {
				return readErr
			}
			defer reader.Close()
			redirectBody, readErr = io.ReadAll(io.LimitReader(reader, faspRequestBodyLimit+1))
			if readErr != nil || int64(len(redirectBody)) > faspRequestBodyLimit {
				return fmt.Errorf("invalid FASP redirect body")
			}
		}
		redirect.Header.Set("Accept", "application/json")
		redirect.Header.Set("Content-Type", "application/json")
		return faspSignHTTPRequest(redirect, redirectBody, privateKey, provider.RemoteIdentifier, time.Now().UTC())
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("FASP provider request failed")
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, faspResponseBodyLimit+1))
	if err != nil || int64(len(responseBody)) > faspResponseBodyLimit {
		return nil, fmt.Errorf("invalid FASP provider response")
	}
	if err := faspVerifyHTTPResponse(response, responseBody, publicKey, time.Now().UTC()); err != nil {
		return nil, err
	}
	return responseBody, nil
}
