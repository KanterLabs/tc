package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	cloudflareJWKSPath     = "/cdn-cgi/access/certs"
	cloudflareHTTPTimeout  = 5 * time.Second
	cloudflareJWKSCacheTTL = time.Hour
	cloudflareMaxJWKSBody  = 1 << 20
	cloudflareMaxAssertion = 64 << 10
)

// CloudflareClaims contains the identity fields used after an Access JWT has
// been cryptographically verified. Name is optional because Access commonly
// includes only the user's verified email in its standard claims.
type CloudflareClaims struct {
	Email string
	Name  string
}

// CloudflareJWTVerifier verifies a Cloudflare Access assertion and returns
// only claims that were covered by its signature and registered-claim checks.
// It is an interface so tests can inject a deterministic verifier without
// making network requests.
type CloudflareJWTVerifier interface {
	Verify(context.Context, string) (CloudflareClaims, error)
}

// JWTVerifier validates Cloudflare Access RS256 assertions against the Access
// team's published JWKS. Keys are cached and refreshed when a token presents
// an unknown key ID, which accommodates normal Cloudflare key rotation.
type JWTVerifier struct {
	issuer    string
	audiences []string
	jwksURL   string
	client    *http.Client

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	refreshMu sync.Mutex
	now       func() time.Time
}

// NewCloudflareJWTVerifier creates a verifier for one Access team and
// application. If jwksURL is empty, the standard Access certs endpoint is
// derived from issuer.
func NewCloudflareJWTVerifier(issuer, audience, jwksURL string) (*JWTVerifier, error) {
	return NewCloudflareJWTVerifierForAudiences(issuer, []string{audience}, jwksURL)
}

// NewCloudflareJWTVerifierForAudiences creates a verifier that accepts an
// assertion for any of the supplied Access application audiences. A Tunnel
// commonly protects the browser UI and /api/v1/* with distinct applications,
// so both audience tags must be configured explicitly. HTTP URLs are accepted
// here only for deterministic httptest callers; production environment
// loading rejects non-HTTPS issuer and JWKS URLs.
func NewCloudflareJWTVerifierForAudiences(issuer string, audiences []string, jwksURL string) (*JWTVerifier, error) {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" {
		return nil, errors.New("Cloudflare issuer is required")
	}
	cleanAudiences := make([]string, 0, len(audiences))
	seen := make(map[string]struct{}, len(audiences))
	for _, audience := range audiences {
		audience = strings.TrimSpace(audience)
		if audience == "" {
			continue
		}
		if _, exists := seen[audience]; exists {
			continue
		}
		seen[audience] = struct{}{}
		cleanAudiences = append(cleanAudiences, audience)
	}
	if len(cleanAudiences) == 0 {
		return nil, errors.New("Cloudflare audience is required")
	}
	if err := validateHTTPURL(issuer); err != nil {
		return nil, fmt.Errorf("invalid Cloudflare issuer: %w", err)
	}
	if strings.TrimSpace(jwksURL) == "" {
		jwksURL = strings.TrimRight(issuer, "/") + cloudflareJWKSPath
	}
	jwksURL = strings.TrimSpace(jwksURL)
	if err := validateHTTPURL(jwksURL); err != nil {
		return nil, fmt.Errorf("invalid Cloudflare JWKS URL: %w", err)
	}
	return &JWTVerifier{
		issuer:    issuer,
		audiences: cleanAudiences,
		jwksURL:   jwksURL,
		client:    cloudflareHTTPClient(),
		now:       time.Now,
	}, nil
}

func cloudflareHTTPClient() *http.Client {
	return &http.Client{
		Timeout: cloudflareHTTPTimeout,
		// The configured certs URL is required to be HTTPS in production;
		// refusing redirects also prevents an endpoint redirect from silently
		// downgrading key retrieval or changing its host.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// NewCloudflareVerifier is a concise compatibility alias for callers that do
// not need to distinguish this implementation from other JWT verifiers.
func NewCloudflareVerifier(issuer, audience, jwksURL string) (*JWTVerifier, error) {
	return NewCloudflareJWTVerifier(issuer, audience, jwksURL)
}

// Verify parses and verifies one compact JWT assertion. It rejects algorithm
// substitution, unknown keys, wrong issuer/audience, invalid time claims, and
// identities without a syntactically valid email address.
func (v *JWTVerifier) Verify(ctx context.Context, assertion string) (CloudflareClaims, error) {
	if v == nil {
		return CloudflareClaims{}, errors.New("Cloudflare verifier is nil")
	}
	if len(assertion) == 0 || len(assertion) > cloudflareMaxAssertion {
		return CloudflareClaims{}, errors.New("invalid Cloudflare assertion size")
	}
	header, payload, signature, signingInput, err := parseCompactJWT(assertion)
	if err != nil {
		return CloudflareClaims{}, err
	}
	if header.Alg != "RS256" || header.Kid == "" {
		return CloudflareClaims{}, errors.New("Cloudflare assertion must use RS256 and a key ID")
	}
	key, err := v.publicKey(ctx, header.Kid)
	if err != nil {
		return CloudflareClaims{}, err
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return CloudflareClaims{}, errors.New("invalid Cloudflare assertion signature")
	}
	claims, err := parseCloudflareClaims(payload)
	if err != nil {
		return CloudflareClaims{}, err
	}
	if err := v.validateClaims(claims); err != nil {
		return CloudflareClaims{}, err
	}
	return claims.Identity, nil
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

func parseCompactJWT(assertion string) (jwtHeader, []byte, []byte, string, error) {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return jwtHeader{}, nil, nil, "", errors.New("invalid Cloudflare assertion format")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwtHeader{}, nil, nil, "", errors.New("invalid Cloudflare assertion header")
	}
	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return jwtHeader{}, nil, nil, "", errors.New("invalid Cloudflare assertion header")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtHeader{}, nil, nil, "", errors.New("invalid Cloudflare assertion payload")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwtHeader{}, nil, nil, "", errors.New("invalid Cloudflare assertion signature")
	}
	return header, payload, signature, parts[0] + "." + parts[1], nil
}

type parsedCloudflareClaims struct {
	Identity  CloudflareClaims
	Issuer    string
	Audience  []string
	Exp       float64
	NotBefore float64
	NBFSet    bool
	IssuedAt  float64
	IATSet    bool
}

func parseCloudflareClaims(payload []byte) (parsedCloudflareClaims, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return parsedCloudflareClaims{}, errors.New("invalid Cloudflare assertion claims")
	}
	if raw == nil {
		return parsedCloudflareClaims{}, errors.New("invalid Cloudflare assertion claims")
	}
	issuer, err := requiredStringClaim(raw, "iss")
	if err != nil {
		return parsedCloudflareClaims{}, err
	}
	email, err := requiredStringClaim(raw, "email")
	if err != nil {
		return parsedCloudflareClaims{}, errors.New("Cloudflare assertion email is required")
	}
	audience, err := audienceClaim(raw["aud"])
	if err != nil {
		return parsedCloudflareClaims{}, err
	}
	exp, _, err := numericClaim(raw, "exp", true)
	if err != nil {
		return parsedCloudflareClaims{}, err
	}
	nbf, nbfSet, err := numericClaim(raw, "nbf", false)
	if err != nil {
		return parsedCloudflareClaims{}, err
	}
	iat, iatSet, err := numericClaim(raw, "iat", false)
	if err != nil {
		return parsedCloudflareClaims{}, err
	}
	name := ""
	if value, ok := raw["name"]; ok {
		if err := json.Unmarshal(value, &name); err != nil {
			return parsedCloudflareClaims{}, errors.New("invalid Cloudflare assertion name")
		}
	}
	if value, ok := raw["type"]; ok {
		var tokenType string
		if err := json.Unmarshal(value, &tokenType); err != nil || (tokenType != "" && tokenType != "app") {
			return parsedCloudflareClaims{}, errors.New("Cloudflare assertion is not an identity token")
		}
	}
	return parsedCloudflareClaims{
		Identity: CloudflareClaims{Email: email, Name: name},
		Issuer:   issuer, Audience: audience, Exp: exp,
		NotBefore: nbf, NBFSet: nbfSet, IssuedAt: iat, IATSet: iatSet,
	}, nil
}

func (v *JWTVerifier) validateClaims(claims parsedCloudflareClaims) error {
	if v.issuer == "" || len(v.audiences) == 0 || claims.Issuer != v.issuer || !validEmail(claims.Identity.Email) {
		return errors.New("invalid Cloudflare identity claims")
	}
	if claims.Identity.Name != "" && (utf8.RuneCountInString(claims.Identity.Name) > 200 || strings.IndexFunc(claims.Identity.Name, unicode.IsControl) >= 0) {
		return errors.New("invalid Cloudflare identity name")
	}
	audienceOK := false
	for _, configured := range v.audiences {
		for _, asserted := range claims.Audience {
			if asserted == configured {
				audienceOK = true
				break
			}
		}
		if audienceOK {
			break
		}
	}
	if !audienceOK {
		return errors.New("invalid Cloudflare identity audience")
	}
	now := float64(v.clock().Unix())
	if claims.Exp <= now {
		return errors.New("Cloudflare assertion has expired")
	}
	if claims.NBFSet && claims.NotBefore > now {
		return errors.New("Cloudflare assertion is not yet valid")
	}
	if claims.IATSet && claims.IssuedAt > now {
		return errors.New("Cloudflare assertion was issued in the future")
	}
	return nil
}

func (v *JWTVerifier) clock() time.Time {
	if v.now != nil {
		return v.now()
	}
	return time.Now()
}

func requiredStringClaim(raw map[string]json.RawMessage, name string) (string, error) {
	value, ok := raw[name]
	if !ok {
		return "", fmt.Errorf("Cloudflare assertion %s is required", name)
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil || strings.TrimSpace(result) == "" {
		return "", fmt.Errorf("Cloudflare assertion %s is invalid", name)
	}
	return result, nil
}

func audienceClaim(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("Cloudflare assertion audience is required")
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if single == "" {
			return nil, errors.New("Cloudflare assertion audience is invalid")
		}
		return []string{single}, nil
	}
	var multiple []string
	if err := json.Unmarshal(raw, &multiple); err != nil || len(multiple) == 0 {
		return nil, errors.New("Cloudflare assertion audience is invalid")
	}
	for _, value := range multiple {
		if value == "" {
			return nil, errors.New("Cloudflare assertion audience is invalid")
		}
	}
	return multiple, nil
}

func numericClaim(raw map[string]json.RawMessage, name string, required bool) (float64, bool, error) {
	value, ok := raw[name]
	if !ok {
		if required {
			return 0, false, fmt.Errorf("Cloudflare assertion %s is required", name)
		}
		return 0, false, nil
	}
	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return 0, false, fmt.Errorf("Cloudflare assertion %s is invalid", name)
	}
	var okNumber bool
	if number, okNumber = decoded.(json.Number); !okNumber {
		return 0, false, fmt.Errorf("Cloudflare assertion %s is invalid", name)
	}
	parsed, err := strconv.ParseFloat(string(number), 64)
	if err != nil || parsed < 0 {
		return 0, false, fmt.Errorf("Cloudflare assertion %s is invalid", name)
	}
	return parsed, true, nil
}

func (v *JWTVerifier) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	now := v.clock()
	v.mu.RLock()
	key := v.keys[kid]
	fetchedAt := v.fetchedAt
	v.mu.RUnlock()
	if key != nil && !fetchedAt.IsZero() && now.Sub(fetchedAt) < cloudflareJWKSCacheTTL {
		return key, nil
	}

	// Serialize refreshes so concurrent unknown-kid requests do not stampede
	// the Access certs endpoint. Recheck after acquiring the lock because a
	// previous waiter may have populated this key already.
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()
	v.mu.RLock()
	key = v.keys[kid]
	fetchedAt = v.fetchedAt
	v.mu.RUnlock()
	if key != nil && !fetchedAt.IsZero() && now.Sub(fetchedAt) < cloudflareJWKSCacheTTL {
		return key, nil
	}
	keys, err := v.fetchKeys(ctx)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = v.clock()
	key = v.keys[kid]
	v.mu.Unlock()
	if key == nil {
		return nil, errors.New("Cloudflare assertion key ID is unknown")
	}
	return key, nil
}

func (v *JWTVerifier) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	requestCtx, cancel := context.WithTimeout(ctx, cloudflareHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, errors.New("could not create Cloudflare JWKS request")
	}
	req.Header.Set("Accept", "application/json")
	client := v.client
	if client == nil {
		client = cloudflareHTTPClient()
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("could not fetch Cloudflare JWKS")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Cloudflare JWKS returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, cloudflareMaxJWKSBody+1))
	if err != nil || len(body) > cloudflareMaxJWKSBody {
		return nil, errors.New("invalid Cloudflare JWKS response size")
	}
	var set struct {
		Keys        []json.RawMessage `json:"keys"`
		PublicCerts []struct {
			Kid  string `json:"kid"`
			Cert string `json:"cert"`
		} `json:"public_certs"`
	}
	if err := json.Unmarshal(body, &set); err != nil || len(set.Keys) == 0 && len(set.PublicCerts) == 0 {
		return nil, errors.New("invalid Cloudflare JWKS response")
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys)+len(set.PublicCerts))
	for _, raw := range set.Keys {
		key, kid, err := parseJWK(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := keys[kid]; exists {
			return nil, errors.New("Cloudflare JWKS contains duplicate key IDs")
		}
		keys[kid] = key
	}
	for _, certificate := range set.PublicCerts {
		key, kid, err := parsePublicCert(certificate.Kid, certificate.Cert)
		if err != nil {
			return nil, err
		}
		if existing, exists := keys[kid]; exists {
			// Cloudflare publishes the same signing keys in both the JWK and
			// PEM lists. Accept that documented representation only when both
			// encodings resolve to the exact same RSA key; conflicting material
			// for one kid must fail closed.
			if existing.E != key.E || existing.N.Cmp(key.N) != 0 {
				return nil, errors.New("Cloudflare JWKS contains conflicting key material")
			}
			continue
		}
		keys[kid] = key
	}
	return keys, nil
}

type jwk struct {
	KTY string   `json:"kty"`
	Use string   `json:"use"`
	Alg string   `json:"alg"`
	Kid string   `json:"kid"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	X5C []string `json:"x5c"`
}

func parseJWK(raw []byte) (*rsa.PublicKey, string, error) {
	var value jwk
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, "", errors.New("invalid Cloudflare JWKS key")
	}
	if value.KTY != "RSA" || value.Kid == "" || (value.Use != "" && value.Use != "sig") || (value.Alg != "" && value.Alg != "RS256") {
		return nil, "", errors.New("unsupported Cloudflare JWKS key")
	}
	if value.N != "" && value.E != "" {
		modulus, err := base64.RawURLEncoding.DecodeString(value.N)
		if err != nil || len(modulus) == 0 {
			return nil, "", errors.New("invalid Cloudflare JWKS modulus")
		}
		exponent, err := base64.RawURLEncoding.DecodeString(value.E)
		if err != nil || len(exponent) == 0 {
			return nil, "", errors.New("invalid Cloudflare JWKS exponent")
		}
		e := new(big.Int).SetBytes(exponent)
		if !e.IsInt64() || e.Int64() < 3 || e.Int64()%2 == 0 {
			return nil, "", errors.New("invalid Cloudflare JWKS exponent")
		}
		modulusInt := new(big.Int).SetBytes(modulus)
		if modulusInt.Sign() <= 0 {
			return nil, "", errors.New("invalid Cloudflare JWKS modulus")
		}
		return &rsa.PublicKey{N: modulusInt, E: int(e.Int64())}, value.Kid, nil
	}
	if len(value.X5C) > 0 {
		der, err := base64.StdEncoding.DecodeString(value.X5C[0])
		if err != nil {
			return nil, "", errors.New("invalid Cloudflare JWKS certificate")
		}
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, "", errors.New("invalid Cloudflare JWKS certificate")
		}
		key, ok := certificate.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, "", errors.New("Cloudflare JWKS certificate is not RSA")
		}
		return key, value.Kid, nil
	}
	return nil, "", errors.New("Cloudflare JWKS key has no RSA material")
}

func parsePublicCert(kid, encoded string) (*rsa.PublicKey, string, error) {
	if strings.TrimSpace(kid) == "" || strings.TrimSpace(encoded) == "" {
		return nil, "", errors.New("invalid Cloudflare JWKS certificate")
	}
	block, rest := pem.Decode([]byte(encoded))
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, "", errors.New("invalid Cloudflare JWKS certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, "", errors.New("invalid Cloudflare JWKS certificate")
	}
	key, ok := certificate.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, "", errors.New("Cloudflare JWKS certificate is not RSA")
	}
	return key, kid, nil
}

func validateHTTPURL(raw string) error {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return errors.New("must be an http(s) URL")
	}
	return nil
}
