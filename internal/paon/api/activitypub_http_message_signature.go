package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/mstdn-plusminus-io/paon/internal/paon/models"
)

type activityHTTPMessageSignatureComponent struct {
	Name       string
	Serialized string
	Parameters map[string]activityStructuredFieldValue
}

type activityHTTPMessageSignatureInput struct {
	Label      string
	Components []activityHTTPMessageSignatureComponent
	Parameters string
	Created    int64
	CreatedSet bool
	Expires    int64
	ExpiresSet bool
	KeyID      string
}

type activityStructuredFieldValue struct {
	kind       byte
	text       string
	integer    int64
	bytes      []byte
	boolean    bool
	serialized string
}

type activityStructuredFieldParser struct {
	raw string
	pos int
}

func (s *Server) activityHTTPMessageSignaturesEnabled() bool {
	return s != nil
}

// signActivityPubHTTPMessageRequest emits an RFC 9421 signature using the
// component set Mastodon 4.5 uses for outbound federation requests. The
// caller passes a non-nil body for requests that must cover Content-Digest.
func (s *Server) signActivityPubHTTPMessageRequest(req *http.Request, account models.Account, body []byte) error {
	key, err := activityPrivateKey(account.PrivateKey.String)
	if err != nil {
		return err
	}
	return s.signActivityPubHTTPMessageRequestWithKey(req, account, key, body)
}

func (s *Server) signActivityPubHTTPMessageRequestWithKey(req *http.Request, account models.Account, key *rsa.PrivateKey, body []byte) error {
	if s == nil || req == nil || req.URL == nil || key == nil {
		return errors.New("HTTP message signature request is incomplete")
	}
	keyID := activityPubActorID(s, account) + "#main-key"
	if strings.ContainsAny(keyID, "\"\\\r\n") {
		return errors.New("HTTP message signature key id is invalid")
	}
	components := []activityHTTPMessageSignatureComponent{
		{Name: "@method", Serialized: `"@method"`},
		{Name: "@target-uri", Serialized: `"@target-uri"`},
	}
	serialized := []string{`"@method"`, `"@target-uri"`}
	if body != nil {
		digest := sha256.Sum256(body)
		req.Header.Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":")
		components = append(components, activityHTTPMessageSignatureComponent{Name: "content-digest", Serialized: `"content-digest"`})
		serialized = append(serialized, `"content-digest"`)
	}
	created := time.Now().UTC().Unix()
	parameters := "(" + strings.Join(serialized, " ") + ");created=" + strconv.FormatInt(created, 10) + `;keyid="` + keyID + `"`
	input := activityHTTPMessageSignatureInput{
		Label: "sig1", Components: components, Parameters: parameters,
		Created: created, CreatedSet: true, KeyID: keyID,
	}
	base, err := buildActivityHTTPMessageSignatureBase(req, input)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(base))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return err
	}
	req.Header.Set("Signature-Input", input.Label+"="+parameters)
	req.Header.Set("Signature", input.Label+"=:"+base64.StdEncoding.EncodeToString(signature)+":")
	return nil
}

func (s *Server) verifyActivityPubHTTPMessageSignature(c *echo.Context, body []byte) (*models.Account, error) {
	if s == nil || c == nil || (*c).Request() == nil {
		return nil, errActivitySignatureFailed
	}
	req := (*c).Request()
	input, err := parseActivityHTTPMessageSignatureInput(activityHTTPHeaderFieldValue(req, "Signature-Input"))
	if err != nil {
		return nil, err
	}
	if input.KeyID == "" || !input.CreatedSet {
		return nil, fmt.Errorf("incompatible request signature: keyid and created are required")
	}
	if err := validateActivityHTTPMessageSignatureTime(input, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := validateActivityHTTPMessageSignatureStrength(req, input); err != nil {
		return nil, err
	}
	if err := validateActivityContentDigest(req, input, body); err != nil {
		return nil, err
	}
	signature, err := parseActivityHTTPMessageSignature(activityHTTPHeaderFieldValue(req, "Signature"), input.Label)
	if err != nil {
		return nil, err
	}
	base, err := buildActivityHTTPMessageSignatureBase(req, input)
	if err != nil {
		return nil, err
	}
	account, err := s.activityPubActorFromKeyIDWithSourceStoplight(c, input.KeyID)
	if err != nil {
		return nil, activityPubSignatureActorResolutionError(err)
	}
	if account == nil {
		return nil, fmt.Errorf("public key not found for key %s", input.KeyID)
	}
	if publicKey, keyErr := activityPublicKey(account.PublicKey); keyErr == nil && verifyActivityHTTPMessageSignature(publicKey, signature, base) == nil {
		return account, nil
	}
	refreshed, err := s.refreshActivityPubActorKeyWithSourceStoplight(c, input.KeyID, account)
	if err != nil {
		return nil, activityPubSignatureActorResolutionError(err)
	}
	if refreshed != nil && refreshed.PublicKey != "" {
		publicKey, keyErr := activityPublicKey(refreshed.PublicKey)
		if keyErr == nil && verifyActivityHTTPMessageSignature(publicKey, signature, base) == nil {
			return refreshed, nil
		}
	}
	return nil, errActivitySignatureFailed
}

func parseActivityHTTPMessageSignatureInput(raw string) (activityHTTPMessageSignatureInput, error) {
	if len(raw) == 0 || len(raw) > 64*1024 {
		return activityHTTPMessageSignatureInput{}, fmt.Errorf("error parsing Signature-Input header")
	}
	p := activityStructuredFieldParser{raw: raw}
	p.skipOWS()
	label, err := p.parseKey()
	if err != nil {
		return activityHTTPMessageSignatureInput{}, fmt.Errorf("error parsing Signature-Input header")
	}
	p.skipOWS()
	if !p.consume('=') {
		return activityHTTPMessageSignatureInput{}, fmt.Errorf("error parsing Signature-Input header")
	}
	p.skipOWS()
	if !p.consume('(') {
		return activityHTTPMessageSignatureInput{}, fmt.Errorf("error parsing Signature-Input header")
	}
	input := activityHTTPMessageSignatureInput{Label: label}
	serializedComponents := make([]string, 0, 4)
	seenComponents := make(map[string]struct{})
	for {
		p.skipSP()
		if p.consume(')') {
			break
		}
		name, serializedName, err := p.parseString()
		if err != nil || name == "" || name != strings.ToLower(name) {
			return activityHTTPMessageSignatureInput{}, fmt.Errorf("error parsing Signature-Input header")
		}
		parameters, serializedParameters, err := p.parseParameters()
		if err != nil {
			return activityHTTPMessageSignatureInput{}, fmt.Errorf("error parsing Signature-Input header")
		}
		serialized := serializedName + serializedParameters
		if _, exists := seenComponents[serialized]; exists {
			return activityHTTPMessageSignatureInput{}, fmt.Errorf("duplicate covered component in Signature-Input header")
		}
		seenComponents[serialized] = struct{}{}
		input.Components = append(input.Components, activityHTTPMessageSignatureComponent{Name: name, Serialized: serialized, Parameters: parameters})
		if len(input.Components) > 64 {
			return activityHTTPMessageSignatureInput{}, fmt.Errorf("too many covered components in Signature-Input header")
		}
		serializedComponents = append(serializedComponents, serialized)
		if p.eof() {
			return activityHTTPMessageSignatureInput{}, fmt.Errorf("error parsing Signature-Input header")
		}
		if p.peek() != ')' && p.peek() != ' ' {
			return activityHTTPMessageSignatureInput{}, fmt.Errorf("error parsing Signature-Input header")
		}
	}
	if len(input.Components) == 0 {
		return activityHTTPMessageSignatureInput{}, fmt.Errorf("error parsing Signature-Input header")
	}
	parameters, serializedParameters, err := p.parseParameters()
	if err != nil {
		return activityHTTPMessageSignatureInput{}, fmt.Errorf("error parsing Signature-Input header")
	}
	input.Parameters = "(" + strings.Join(serializedComponents, " ") + ")" + serializedParameters
	if value, ok := parameters["created"]; ok {
		if value.kind != 'i' {
			return activityHTTPMessageSignatureInput{}, fmt.Errorf("invalid created signature parameter")
		}
		input.Created, input.CreatedSet = value.integer, true
	}
	if value, ok := parameters["expires"]; ok {
		if value.kind != 'i' {
			return activityHTTPMessageSignatureInput{}, fmt.Errorf("invalid expires signature parameter")
		}
		input.Expires, input.ExpiresSet = value.integer, true
	}
	if value, ok := parameters["keyid"]; ok {
		if value.kind != 's' {
			return activityHTTPMessageSignatureInput{}, fmt.Errorf("invalid keyid signature parameter")
		}
		input.KeyID = value.text
	}
	p.skipOWS()
	if !p.eof() {
		// Linzer builds a single signature from the request. Reject an
		// ambiguous dictionary instead of sharing selection state globally.
		return activityHTTPMessageSignatureInput{}, fmt.Errorf("multiple Signature-Input members are not supported")
	}
	return input, nil
}

func parseActivityHTTPMessageSignature(raw string, label string) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 64*1024 {
		return nil, fmt.Errorf("error parsing Signature header")
	}
	p := activityStructuredFieldParser{raw: raw}
	seen := make(map[string]struct{})
	var found []byte
	for {
		p.skipOWS()
		if p.eof() {
			break
		}
		memberLabel, err := p.parseKey()
		if err != nil {
			return nil, fmt.Errorf("error parsing Signature header")
		}
		if _, exists := seen[memberLabel]; exists {
			return nil, fmt.Errorf("duplicate Signature member")
		}
		seen[memberLabel] = struct{}{}
		p.skipOWS()
		if !p.consume('=') {
			return nil, fmt.Errorf("error parsing Signature header")
		}
		p.skipOWS()
		value, err := p.parseBareItem()
		if err != nil || value.kind != 'b' {
			return nil, fmt.Errorf("error parsing Signature header")
		}
		if _, _, err := p.parseParameters(); err != nil {
			return nil, fmt.Errorf("error parsing Signature header")
		}
		if memberLabel == label {
			found = append([]byte(nil), value.bytes...)
		}
		p.skipOWS()
		if p.eof() {
			break
		}
		if !p.consume(',') {
			return nil, fmt.Errorf("error parsing Signature header")
		}
		p.skipOWS()
		if p.eof() {
			return nil, fmt.Errorf("error parsing Signature header")
		}
	}
	if found != nil {
		return found, nil
	}
	return nil, fmt.Errorf("signature label %s is missing", label)
}

func validateActivityHTTPMessageSignatureTime(input activityHTTPMessageSignatureInput, now time.Time) error {
	if !input.CreatedSet {
		return fmt.Errorf("mastodon requires the (created) parameter to be signed")
	}
	created := time.Unix(input.Created, 0).UTC()
	expires := created.Add(5 * time.Minute)
	if input.ExpiresSet {
		expires = time.Unix(input.Expires, 0).UTC()
	}
	if maximum := created.Add(activitySignatureExpirationWindow); expires.After(maximum) {
		expires = maximum
	}
	if created.After(now.Add(activitySignatureClockSkew)) || now.After(expires.Add(activitySignatureClockSkew)) {
		return fmt.Errorf("signed request date outside acceptable time window")
	}
	return nil
}

func validateActivityHTTPMessageSignatureStrength(req *http.Request, input activityHTTPMessageSignatureInput) error {
	if !activityHTTPMessageSignatureHasComponent(input, "@method") || !activityHTTPMessageSignatureHasComponent(input, "@target-uri") {
		return fmt.Errorf("mastodon requires the @method and @target-uri derived components to be signed")
	}
	if req != nil && req.Method == http.MethodPost && !activityHTTPMessageSignatureHasComponent(input, "content-digest") {
		return fmt.Errorf("mastodon requires the Content-Digest header to be signed when doing a POST request")
	}
	return nil
}

func activityHTTPMessageSignatureHasComponent(input activityHTTPMessageSignatureInput, name string) bool {
	for _, component := range input.Components {
		if component.Name == name {
			return true
		}
	}
	return false
}

func validateActivityContentDigest(req *http.Request, input activityHTTPMessageSignatureInput, body []byte) error {
	if !activityHTTPMessageSignatureHasComponent(input, "content-digest") {
		return nil
	}
	if req == nil || strings.TrimSpace(activityHTTPHeaderFieldValue(req, "Content-Digest")) == "" {
		return fmt.Errorf("content-digest header missing")
	}
	digests, err := parseActivityContentDigest(activityHTTPHeaderFieldValue(req, "Content-Digest"))
	if err != nil {
		return fmt.Errorf("%w: Content-Digest does not contain a valid RFC 8941 dictionary", errActivitySignatureMalformed)
	}
	provided, ok := digests["sha-256"]
	if !ok {
		return fmt.Errorf("mastodon only supports SHA-256 in Content-Digest header")
	}
	want := sha256.Sum256(body)
	if !equalActivityBytes(provided, want[:]) {
		return fmt.Errorf("invalid Content-Digest value")
	}
	return nil
}

func parseActivityContentDigest(raw string) (map[string][]byte, error) {
	if len(raw) == 0 || len(raw) > 16*1024 {
		return nil, fmt.Errorf("invalid digest dictionary")
	}
	p := activityStructuredFieldParser{raw: raw}
	result := make(map[string][]byte)
	for {
		p.skipOWS()
		if p.eof() {
			break
		}
		algorithm, err := p.parseKey()
		if err != nil {
			return nil, err
		}
		if _, exists := result[algorithm]; exists {
			return nil, fmt.Errorf("duplicate digest algorithm")
		}
		p.skipOWS()
		if !p.consume('=') {
			return nil, fmt.Errorf("missing digest value")
		}
		p.skipOWS()
		value, err := p.parseBareItem()
		if err != nil || value.kind != 'b' {
			return nil, fmt.Errorf("invalid digest value")
		}
		if _, _, err := p.parseParameters(); err != nil {
			return nil, err
		}
		result[algorithm] = append([]byte(nil), value.bytes...)
		p.skipOWS()
		if p.eof() {
			break
		}
		if !p.consume(',') {
			return nil, fmt.Errorf("invalid digest dictionary")
		}
		p.skipOWS()
		if p.eof() {
			return nil, fmt.Errorf("invalid digest dictionary")
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("empty digest dictionary")
	}
	return result, nil
}

func buildActivityHTTPMessageSignatureBase(req *http.Request, input activityHTTPMessageSignatureInput) (string, error) {
	lines := make([]string, 0, len(input.Components)+1)
	for _, component := range input.Components {
		value, err := activityHTTPMessageSignatureComponentValue(req, component)
		if err != nil {
			return "", err
		}
		lines = append(lines, component.Serialized+": "+value)
	}
	lines = append(lines, `"@signature-params": `+input.Parameters)
	return strings.Join(lines, "\n"), nil
}

func activityHTTPMessageSignatureComponentValue(req *http.Request, component activityHTTPMessageSignatureComponent) (string, error) {
	if req == nil || req.URL == nil {
		return "", fmt.Errorf("request is unavailable")
	}
	if len(component.Parameters) != 0 {
		return "", fmt.Errorf("unsupported HTTP message signature component parameters")
	}
	switch component.Name {
	case "@method":
		return strings.ToUpper(req.Method), nil
	case "@target-uri":
		return activityHTTPMessageSignatureTargetURI(req), nil
	case "@authority":
		return activityHTTPRequestAuthority(req), nil
	case "@scheme":
		return activityHTTPRequestScheme(req), nil
	case "@request-target":
		return req.URL.RequestURI(), nil
	case "@path":
		path := req.URL.EscapedPath()
		if path == "" {
			path = "/"
		}
		return path, nil
	case "@query":
		return "?" + req.URL.RawQuery, nil
	default:
		if strings.HasPrefix(component.Name, "@") || component.Name != strings.ToLower(component.Name) {
			return "", fmt.Errorf("unsupported HTTP message signature component %s", component.Name)
		}
		if component.Name == "host" {
			return activityHTTPRequestAuthority(req), nil
		}
		value := activityHTTPHeaderFieldValue(req, component.Name)
		if value == "" && len(req.Header.Values(http.CanonicalHeaderKey(component.Name))) == 0 {
			return "", fmt.Errorf("covered header %s is missing", component.Name)
		}
		return value, nil
	}
}

func activityHTTPHeaderFieldValue(req *http.Request, name string) string {
	if req == nil {
		return ""
	}
	values := req.Header.Values(http.CanonicalHeaderKey(name))
	for i := range values {
		values[i] = strings.Trim(values[i], " \t")
	}
	return strings.Join(values, ", ")
}

func activityHTTPMessageSignatureTargetURI(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	if req.URL.IsAbs() {
		copyURL := *req.URL
		copyURL.Fragment = ""
		return copyURL.String()
	}
	return activityHTTPRequestScheme(req) + "://" + activityHTTPRequestAuthority(req) + req.URL.RequestURI()
}

func activityHTTPRequestScheme(req *http.Request) string {
	if req == nil {
		return ""
	}
	if req.URL != nil && req.URL.Scheme != "" {
		return strings.ToLower(req.URL.Scheme)
	}
	if req.TLS != nil {
		return "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(req.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded != "" {
		return strings.ToLower(forwarded)
	}
	return "http"
}

func activityHTTPRequestAuthority(req *http.Request) string {
	if req == nil {
		return ""
	}
	if strings.TrimSpace(req.Host) != "" {
		return req.Host
	}
	if req.URL != nil {
		return req.URL.Host
	}
	return ""
}

func verifyActivityHTTPMessageSignature(key *rsa.PublicKey, signature []byte, base string) error {
	if key == nil || len(signature) == 0 {
		return errActivitySignatureFailed
	}
	digest := sha256.Sum256([]byte(base))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature)
}

func equalActivityBytes(left []byte, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for i := range left {
		difference |= left[i] ^ right[i]
	}
	return difference == 0
}

func (p *activityStructuredFieldParser) eof() bool { return p.pos >= len(p.raw) }

func (p *activityStructuredFieldParser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.raw[p.pos]
}

func (p *activityStructuredFieldParser) consume(want byte) bool {
	if p.peek() != want {
		return false
	}
	p.pos++
	return true
}

func (p *activityStructuredFieldParser) skipOWS() {
	for !p.eof() && (p.peek() == ' ' || p.peek() == '\t') {
		p.pos++
	}
}

func (p *activityStructuredFieldParser) skipSP() {
	for !p.eof() && p.peek() == ' ' {
		p.pos++
	}
}

func (p *activityStructuredFieldParser) parseKey() (string, error) {
	start := p.pos
	if p.eof() || !activityStructuredFieldKeyFirst(p.peek()) {
		return "", fmt.Errorf("invalid structured field key")
	}
	p.pos++
	for !p.eof() && activityStructuredFieldKeyRest(p.peek()) {
		p.pos++
	}
	return p.raw[start:p.pos], nil
}

func activityStructuredFieldKeyFirst(value byte) bool {
	return (value >= 'a' && value <= 'z') || value == '*'
}

func activityStructuredFieldKeyRest(value byte) bool {
	return activityStructuredFieldKeyFirst(value) || (value >= '0' && value <= '9') || strings.ContainsRune("_-.", rune(value))
}

func (p *activityStructuredFieldParser) parseString() (string, string, error) {
	if !p.consume('"') {
		return "", "", fmt.Errorf("expected string")
	}
	var value strings.Builder
	var serialized strings.Builder
	serialized.WriteByte('"')
	for !p.eof() {
		char := p.peek()
		p.pos++
		switch char {
		case '"':
			serialized.WriteByte('"')
			return value.String(), serialized.String(), nil
		case '\\':
			if p.eof() || (p.peek() != '\\' && p.peek() != '"') {
				return "", "", fmt.Errorf("invalid string escape")
			}
			escaped := p.peek()
			p.pos++
			value.WriteByte(escaped)
			serialized.WriteByte('\\')
			serialized.WriteByte(escaped)
		default:
			if char < 0x20 || char > 0x7e {
				return "", "", fmt.Errorf("invalid string character")
			}
			value.WriteByte(char)
			serialized.WriteByte(char)
		}
	}
	return "", "", fmt.Errorf("unterminated string")
}

func (p *activityStructuredFieldParser) parseParameters() (map[string]activityStructuredFieldValue, string, error) {
	parameters := make(map[string]activityStructuredFieldValue)
	var serialized strings.Builder
	for p.consume(';') {
		name, err := p.parseKey()
		if err != nil {
			return nil, "", err
		}
		if _, exists := parameters[name]; exists {
			return nil, "", fmt.Errorf("duplicate structured field parameter")
		}
		value := activityStructuredFieldValue{kind: 'B', boolean: true, serialized: "?1"}
		if p.consume('=') {
			value, err = p.parseBareItem()
			if err != nil {
				return nil, "", err
			}
		}
		parameters[name] = value
		serialized.WriteByte(';')
		serialized.WriteString(name)
		if value.kind != 'B' || !value.boolean {
			serialized.WriteByte('=')
			serialized.WriteString(value.serialized)
		}
	}
	return parameters, serialized.String(), nil
}

func (p *activityStructuredFieldParser) parseBareItem() (activityStructuredFieldValue, error) {
	if p.eof() {
		return activityStructuredFieldValue{}, fmt.Errorf("missing structured field value")
	}
	switch p.peek() {
	case '"':
		text, serialized, err := p.parseString()
		return activityStructuredFieldValue{kind: 's', text: text, serialized: serialized}, err
	case ':':
		p.pos++
		start := p.pos
		for !p.eof() && p.peek() != ':' {
			char := p.peek()
			if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '+' || char == '/' || char == '=') {
				return activityStructuredFieldValue{}, fmt.Errorf("invalid byte sequence")
			}
			p.pos++
		}
		if !p.consume(':') {
			return activityStructuredFieldValue{}, fmt.Errorf("unterminated byte sequence")
		}
		encoded := p.raw[start : p.pos-1]
		decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return activityStructuredFieldValue{}, fmt.Errorf("invalid byte sequence")
		}
		return activityStructuredFieldValue{kind: 'b', bytes: decoded, serialized: ":" + base64.StdEncoding.EncodeToString(decoded) + ":"}, nil
	case '?':
		p.pos++
		if p.eof() || (p.peek() != '0' && p.peek() != '1') {
			return activityStructuredFieldValue{}, fmt.Errorf("invalid boolean")
		}
		value := p.peek() == '1'
		p.pos++
		serialized := "?0"
		if value {
			serialized = "?1"
		}
		return activityStructuredFieldValue{kind: 'B', boolean: value, serialized: serialized}, nil
	default:
		if p.peek() == '-' || (p.peek() >= '0' && p.peek() <= '9') {
			start := p.pos
			if p.consume('-') && (p.eof() || p.peek() < '0' || p.peek() > '9') {
				return activityStructuredFieldValue{}, fmt.Errorf("invalid integer")
			}
			for !p.eof() && p.peek() >= '0' && p.peek() <= '9' {
				p.pos++
			}
			raw := p.raw[start:p.pos]
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return activityStructuredFieldValue{}, fmt.Errorf("invalid integer")
			}
			return activityStructuredFieldValue{kind: 'i', integer: value, serialized: strconv.FormatInt(value, 10)}, nil
		}
		start := p.pos
		if !activityStructuredFieldTokenFirst(p.peek()) {
			return activityStructuredFieldValue{}, fmt.Errorf("invalid token")
		}
		p.pos++
		for !p.eof() && activityStructuredFieldTokenRest(p.peek()) {
			p.pos++
		}
		value := p.raw[start:p.pos]
		return activityStructuredFieldValue{kind: 't', text: value, serialized: value}, nil
	}
}

func activityStructuredFieldTokenFirst(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z') || value == '*'
}

func activityStructuredFieldTokenRest(value byte) bool {
	return activityStructuredFieldTokenFirst(value) || (value >= '0' && value <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~:/", rune(value))
}

func activityPubSignatureActorResolutionError(err error) error {
	if err == nil || !activityPubSignatureTemporaryFailure(err) {
		return err
	}
	return fmt.Errorf("%w: %v", errActivitySignatureTemporary, err)
}

func activityPubSignatureTemporaryFailure(err error) bool {
	if err == nil {
		return false
	}
	if status, ok := activityFetchStatus(err); ok {
		return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
	}
	var netError interface{ Timeout() bool }
	if errors.As(err, &netError) && netError.Timeout() {
		return true
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "temporary problem") || strings.Contains(message, "connection failure") || strings.Contains(message, "failed to fetch remote activity")
}
