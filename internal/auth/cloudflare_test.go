package auth

import (
	"context"
	"crypto"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"roadmap/internal/config"
	"roadmap/internal/db"
	"roadmap/internal/store"
)

type testSigningKey struct {
	private *rsa.PrivateKey
	kid     string
}

func newTestSigningKey(t *testing.T, kid string) testSigningKey {
	t.Helper()
	private, err := rsa.GenerateKey(cryptorand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return testSigningKey{private: private, kid: kid}
}

func jwksForKey(t *testing.T, key testSigningKey) []byte {
	t.Helper()
	encode := func(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }
	body, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": key.kid,
		"n":   encode(key.private.PublicKey.N.Bytes()),
		"e":   encode(bigIntBytes(int64(key.private.PublicKey.E))),
	}}})
	if err != nil {
		t.Fatalf("marshal JWKS: %v", err)
	}
	return body
}

func jwksForKeyAndCertificate(t *testing.T, key testSigningKey) []byte {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Unix(1_999_999_000, 0),
		NotAfter:     time.Unix(2_000_001_000, 0),
	}
	der, err := x509.CreateCertificate(cryptorand.Reader, template, template, &key.private.PublicKey, key.private)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(jwksForKey(t, key), &document); err != nil {
		t.Fatalf("decode JWK set: %v", err)
	}
	document["public_certs"] = []map[string]string{{"kid": key.kid, "cert": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}}
	body, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal JWK and certificate set: %v", err)
	}
	return body
}

func bigIntBytes(value int64) []byte {
	if value == 0 {
		return []byte{0}
	}
	result := make([]byte, 0, 8)
	for value > 0 {
		result = append([]byte{byte(value)}, result...)
		value >>= 8
	}
	return result
}

func signedJWT(t *testing.T, key testSigningKey, claims map[string]any, algorithm string) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": algorithm, "kid": key.kid, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	input := encode(header) + "." + encode(payload)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(cryptorand.Reader, key.private, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + encode(signature)
}

func validClaims(now time.Time, audience any) map[string]any {
	return map[string]any{
		"iss":   "https://team.example",
		"aud":   audience,
		"email": "owner@example.com",
		"name":  "Owner",
		"type":  "app",
		"exp":   now.Add(time.Minute).Unix(),
		"iat":   now.Add(-time.Minute).Unix(),
		"nbf":   now.Add(-time.Minute).Unix(),
	}
}

func testJWKSVerifier(t *testing.T, key testSigningKey, audiences ...string) (*JWTVerifier, *httptest.Server, *atomic.Int32) {
	t.Helper()
	body := jwksForKey(t, key)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/certs" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	verifier, err := NewCloudflareJWTVerifierForAudiences("https://team.example", audiences, server.URL+"/certs")
	if err != nil {
		t.Fatalf("create verifier: %v", err)
	}
	verifier.now = func() time.Time { return time.Unix(2_000_000_000, 0) }
	return verifier, server, &requests
}

func TestCloudflareJWTVerifierAcceptsSignedIdentityAndCachesKeys(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	key := newTestSigningKey(t, "kid-1")
	verifier, _, requests := testJWKSVerifier(t, key, "ui-audience", "api-audience")
	claims := validClaims(now, []string{"ui-audience"})
	assertion := signedJWT(t, key, claims, "RS256")
	identity, err := verifier.Verify(context.Background(), assertion)
	if err != nil {
		t.Fatalf("verify UI assertion: %v", err)
	}
	if identity.Email != "owner@example.com" || identity.Name != "Owner" {
		t.Fatalf("identity = %#v", identity)
	}

	claims = validClaims(now, []string{"api-audience"})
	identity, err = verifier.Verify(context.Background(), signedJWT(t, key, claims, "RS256"))
	if err != nil {
		t.Fatalf("verify API assertion: %v", err)
	}
	if identity.Email != "owner@example.com" {
		t.Fatalf("API identity = %#v", identity)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("JWKS requests = %d, want cached single fetch", got)
	}
	verifier.mu.Lock()
	verifier.fetchedAt = now.Add(-cloudflareJWKSCacheTTL - time.Second)
	verifier.mu.Unlock()
	if _, err := verifier.Verify(context.Background(), signedJWT(t, key, validClaims(now, "ui-audience"), "RS256")); err != nil {
		t.Fatalf("verify after cache expiry: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("JWKS requests after cache expiry = %d, want refresh", got)
	}
}

func TestCloudflareJWTVerifierAcceptsDocumentedJWKAndPEMKeySet(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	key := newTestSigningKey(t, "kid-both")
	body := jwksForKeyAndCertificate(t, key)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	verifier, err := NewCloudflareJWTVerifier("https://team.example", "ui-audience", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	verifier.now = func() time.Time { return now }
	if _, err := verifier.Verify(context.Background(), signedJWT(t, key, validClaims(now, "ui-audience"), "RS256")); err != nil {
		t.Fatalf("verify documented key set: %v", err)
	}
}

func TestCloudflareJWTVerifierRefreshesOnUnknownKeyID(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	first := newTestSigningKey(t, "kid-1")
	second := newTestSigningKey(t, "kid-2")
	var mu sync.RWMutex
	body := jwksForKey(t, first)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		mu.RLock()
		defer mu.RUnlock()
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	verifier, err := NewCloudflareJWTVerifierForAudiences("https://team.example", []string{"ui-audience"}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	verifier.now = func() time.Time { return now }
	if _, err := verifier.Verify(context.Background(), signedJWT(t, first, validClaims(now, "ui-audience"), "RS256")); err != nil {
		t.Fatalf("verify first key: %v", err)
	}
	mu.Lock()
	body = jwksForKey(t, second)
	mu.Unlock()
	if _, err := verifier.Verify(context.Background(), signedJWT(t, second, validClaims(now, "ui-audience"), "RS256")); err != nil {
		t.Fatalf("verify rotated key: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("JWKS requests = %d, want refresh after unknown kid", got)
	}
}

func TestCloudflareJWTVerifierCacheIsSafeForConcurrentVerification(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	key := newTestSigningKey(t, "kid-concurrent")
	verifier, _, requests := testJWKSVerifier(t, key, "ui-audience")
	assertion := signedJWT(t, key, validClaims(now, "ui-audience"), "RS256")
	var group sync.WaitGroup
	errorsCh := make(chan error, 32)
	for i := 0; i < cap(errorsCh); i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := verifier.Verify(context.Background(), assertion)
			errorsCh <- err
		}()
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("concurrent verification: %v", err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("concurrent JWKS requests = %d, want one", got)
	}
}

func TestCloudflareJWTVerifierRejectsInvalidAssertions(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	key := newTestSigningKey(t, "kid-1")
	verifier, _, _ := testJWKSVerifier(t, key, "ui-audience")

	tests := []struct {
		name      string
		algorithm string
		mutate    func(map[string]any)
	}{
		{name: "unsigned", algorithm: "none"},
		{name: "wrong algorithm", algorithm: "HS256"},
		{name: "wrong audience", algorithm: "RS256", mutate: func(claims map[string]any) { claims["aud"] = "other-audience" }},
		{name: "wrong issuer", algorithm: "RS256", mutate: func(claims map[string]any) { claims["iss"] = "https://other.example" }},
		{name: "expired", algorithm: "RS256", mutate: func(claims map[string]any) { claims["exp"] = now.Unix() - 1 }},
		{name: "not before", algorithm: "RS256", mutate: func(claims map[string]any) { claims["nbf"] = now.Unix() + 1 }},
		{name: "issued in future", algorithm: "RS256", mutate: func(claims map[string]any) { claims["iat"] = now.Unix() + 1 }},
		{name: "service token", algorithm: "RS256", mutate: func(claims map[string]any) { claims["type"] = "service-token" }},
		{name: "service token shape", algorithm: "RS256", mutate: func(claims map[string]any) {
			delete(claims, "email")
			claims["common_name"] = "roadmap-service-token"
		}},
		{name: "invalid email", algorithm: "RS256", mutate: func(claims map[string]any) { claims["email"] = "not-an-email" }},
		{name: "oversized name", algorithm: "RS256", mutate: func(claims map[string]any) { claims["name"] = strings.Repeat("n", 201) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := validClaims(now, "ui-audience")
			if test.mutate != nil {
				test.mutate(claims)
			}
			if _, err := verifier.Verify(context.Background(), signedJWT(t, key, claims, test.algorithm)); err == nil {
				t.Fatal("invalid assertion unexpectedly accepted")
			}
		})
	}
}

type staticVerifier struct {
	claims CloudflareClaims
	err    error
}

func (v staticVerifier) Verify(context.Context, string) (CloudflareClaims, error) {
	return v.claims, v.err
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return store.New(database)
}

func TestManagerCloudflareUsesOnlyVerifiedAssertion(t *testing.T) {
	data := testStore(t)
	manager := NewManagerWithVerifier(data, config.Config{AuthMode: "cloudflare", AdminEmail: "owner@example.com"}, staticVerifier{claims: CloudflareClaims{Email: "owner@example.com", Name: "Owner"}})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	request.Header.Set("X-Auth-Request-Email", "owner@example.com")
	request.Header.Set("Cf-Access-Authenticated-User-Email", "owner@example.com")
	if _, err := manager.Authenticate(context.Background(), request); err == nil {
		t.Fatal("ordinary identity headers forged a Cloudflare identity")
	}

	request.Header.Set("Cf-Access-Jwt-Assertion", "test-assertion")
	identity, err := manager.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatalf("verified assertion rejected: %v", err)
	}
	if identity.Actor.EmailValue() != "owner@example.com" || !identity.Actor.Admin {
		t.Fatalf("actor = %#v", identity.Actor)
	}
}

func TestManagerLoginOnlyAvailableInLocalMode(t *testing.T) {
	data := testStore(t)
	manager := NewManagerWithVerifier(data, config.Config{AuthMode: "cloudflare"}, staticVerifier{err: errors.New("not used")})
	if _, err := manager.Login(context.Background(), "owner@example.com", "correct horse battery staple", httptest.NewRecorder()); !errors.Is(err, store.ErrForbidden) {
		t.Fatalf("cloudflare login error = %v, want forbidden", err)
	}
}

func TestPasswordMinimumLength(t *testing.T) {
	if _, err := HashPassword("12345678901"); err == nil {
		t.Fatal("11-character password unexpectedly accepted")
	}
	if _, err := HashPassword("123456789012"); err != nil {
		t.Fatalf("12-character password rejected: %v", err)
	}
}

func TestManagerBearerTokenTakesPrecedence(t *testing.T) {
	data := testStore(t)
	actor, err := data.CreateActor(context.Background(), store.Actor{Kind: "agent", Name: "agent"}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, plaintext, err := data.CreateTokenBy(context.Background(), actor.ID, actor.ID, "test", []string{"tasks:read"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManagerWithVerifier(data, config.Config{AuthMode: "cloudflare"}, staticVerifier{err: fmt.Errorf("assertion should not run")})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+plaintext)
	request.Header.Set("Cf-Access-Jwt-Assertion", "invalid")
	identity, err := manager.Authenticate(context.Background(), request)
	if err != nil || !identity.IsToken || identity.Actor.ID != actor.ID {
		t.Fatalf("bearer precedence identity=%#v err=%v", identity, err)
	}
}
