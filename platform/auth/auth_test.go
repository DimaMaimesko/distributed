package auth

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"distributed/platform/middleware"
)

const (
	testIssuer   = "https://auth.test"
	testAudience = "orders"
)

var testIdentity = Identity{
	UserID: "user-123",
	Email:  "test@example.com",
	Role:   "customer",
	Scopes: []string{"orders:read"},
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newSigner(t *testing.T, key *ecdsa.PrivateKey, issuer string, aud []string, ttl time.Duration) *Signer {
	t.Helper()
	s, err := NewSigner(key, issuer, aud, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func issue(t *testing.T, s *Signer, id Identity) string {
	t.Helper()
	tok, err := s.Issue(id)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func staticValidator(t *testing.T, keys ...*ecdsa.PublicKey) *Validator {
	t.Helper()
	ks, err := NewStaticKeys(keys...)
	if err != nil {
		t.Fatal(err)
	}
	return NewValidator(ks, testIssuer, testAudience)
}

func TestIssueAndValidate(t *testing.T) {
	key := newKey(t)
	signer := newSigner(t, key, testIssuer, []string{"orders", "inventory"}, time.Hour)
	tok := issue(t, signer, testIdentity)

	claims, err := staticValidator(t, &key.PublicKey).Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.UserID() != "user-123" || claims.Email != "test@example.com" ||
		claims.Role != "customer" || !claims.Scope.Has("orders:read") || claims.ID == "" {
		t.Errorf("claims = %+v", claims)
	}

	// The wire format follows the conventions other JWT tooling expects.
	parts := strings.Split(tok, ".")
	var header map[string]any
	var payload map[string]any
	decodeSegment(t, parts[0], &header)
	decodeSegment(t, parts[1], &payload)
	if kid, _ := Thumbprint(&key.PublicKey); header["kid"] != kid || header["alg"] != "ES256" {
		t.Errorf("header = %v", header)
	}
	if payload["scope"] != "orders:read" || payload["sub"] != "user-123" {
		t.Errorf("payload = %v", payload)
	}
}

func decodeSegment(t *testing.T, seg string, v any) {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_Rejects(t *testing.T) {
	key := newKey(t)
	other := newKey(t)
	kid, _ := Thumbprint(&key.PublicKey)
	v := staticValidator(t, &key.PublicKey)

	// forge signs claims with an arbitrary method and key, claiming kid.
	forge := func(method jwt.SigningMethod, signKey any) string {
		claims := Claims{RegisteredClaims: jwt.RegisteredClaims{
			Issuer: testIssuer, Subject: "attacker", Audience: jwt.ClaimStrings{testAudience},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}}
		tok := jwt.NewWithClaims(method, claims)
		tok.Header["kid"] = kid
		s, err := tok.SignedString(signKey)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	pubDER, _ := (&key.PublicKey).Bytes()
	valid := issue(t, newSigner(t, key, testIssuer, []string{testAudience}, time.Hour), testIdentity)

	tests := []struct {
		name  string
		token string
		want  error // nil means any error
	}{
		{"expired",
			issue(t, newSigner(t, key, testIssuer, []string{testAudience}, -time.Hour), testIdentity),
			jwt.ErrTokenExpired},
		{"wrong issuer",
			issue(t, newSigner(t, key, "https://auth.staging", []string{testAudience}, time.Hour), testIdentity),
			jwt.ErrTokenInvalidIssuer},
		{"wrong audience",
			issue(t, newSigner(t, key, testIssuer, []string{"billing"}, time.Hour), testIdentity),
			jwt.ErrTokenInvalidAudience},
		{"unknown key",
			issue(t, newSigner(t, other, testIssuer, []string{testAudience}, time.Hour), testIdentity),
			ErrUnknownKey},
		{"other key claiming our kid", forge(jwt.SigningMethodES256, other), jwt.ErrTokenSignatureInvalid},
		{"alg none", forge(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType), nil},
		{"HS256 keyed with the public key", forge(jwt.SigningMethodHS256, pubDER), nil},
		{"tampered payload", tamper(t, valid), jwt.ErrTokenSignatureInvalid},
		{"non-canonical signature encoding", respell(t, valid), nil},
		{"garbage", "not.a.token", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := v.Validate(context.Background(), tt.token)
			if err == nil {
				t.Fatal("token accepted")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// tamper swaps the payload for one with a different subject, keeping the
// original signature.
func tamper(t *testing.T, tok string) string {
	parts := strings.Split(tok, ".")
	var payload map[string]any
	decodeSegment(t, parts[1], &payload)
	payload["sub"] = "admin"
	b, _ := json.Marshal(payload)
	parts[1] = base64.RawURLEncoding.EncodeToString(b)
	return strings.Join(parts, ".")
}

// respell changes the last signature character without changing the
// decoded signature bytes, by setting the unused low bits.
func respell(t *testing.T, tok string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	i := strings.IndexByte(alphabet, tok[len(tok)-1])
	// 86 characters carry 516 bits for a 512-bit signature: the last
	// character's low 4 bits are padding.
	return tok[:len(tok)-1] + string(alphabet[i|0x0f])
}

func TestValidate_ToleratesClockSkew(t *testing.T) {
	key := newKey(t)
	tok := issue(t, newSigner(t, key, testIssuer, []string{testAudience}, -10*time.Second), testIdentity)
	if _, err := staticValidator(t, &key.PublicKey).Validate(context.Background(), tok); err != nil {
		t.Errorf("token 10s past expiry rejected: %v", err)
	}
}

func TestJWK_RoundTrip(t *testing.T) {
	// About 1 key in 256 has an X coordinate starting with a zero byte;
	// encoding it with big.Int.Bytes() would produce an invalid JWK.
	var key *ecdsa.PrivateKey
	for range 20000 {
		k := newKey(t)
		if b, _ := k.PublicKey.Bytes(); b[1] == 0 {
			key = k
			break
		}
	}
	if key == nil {
		t.Fatal("no key with a leading zero byte generated")
	}

	jwk, err := NewJWK(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if x, _ := base64.RawURLEncoding.DecodeString(jwk.X); len(x) != 32 {
		t.Errorf("x is %d bytes, want 32", len(x))
	}
	pub, err := jwk.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(&key.PublicKey) {
		t.Error("decoded key differs")
	}
	if jwk.KeyID != jwk.thumbprint() || jwk.KeyID == "" {
		t.Errorf("kid = %q", jwk.KeyID)
	}
}

// jwksServer serves the given keys and counts requests; keys can be
// swapped while it runs.
type jwksServer struct {
	*httptest.Server
	mu   sync.Mutex
	keys []*ecdsa.PublicKey
	hits atomic.Int32
	down atomic.Bool
}

func newJWKSServer(t *testing.T, keys ...*ecdsa.PublicKey) *jwksServer {
	s := &jwksServer{keys: keys}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		if s.down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		s.mu.Lock()
		h, err := JWKSHandler(s.keys...)
		s.mu.Unlock()
		if err != nil {
			t.Error(err)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *jwksServer) setKeys(keys ...*ecdsa.PublicKey) {
	s.mu.Lock()
	s.keys = keys
	s.mu.Unlock()
}

func TestJWKSClient_Rotation(t *testing.T) {
	oldKey, newKeyPair := newKey(t), newKey(t)
	srv := newJWKSServer(t, &oldKey.PublicKey)

	client := NewJWKSClient(srv.URL)
	if err := client.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	v := NewValidator(client, testIssuer, testAudience)

	oldTok := issue(t, newSigner(t, oldKey, testIssuer, []string{testAudience}, time.Hour), testIdentity)
	if _, err := v.Validate(context.Background(), oldTok); err != nil {
		t.Fatalf("old key: %v", err)
	}

	// Rotation: publish the new key next to the old one, then sign with it.
	srv.setKeys(&newKeyPair.PublicKey, &oldKey.PublicKey)
	newTok := issue(t, newSigner(t, newKeyPair, testIssuer, []string{testAudience}, time.Hour), testIdentity)

	// Within the rate limit the new kid is not fetched yet...
	if _, err := v.Validate(context.Background(), newTok); err == nil {
		t.Fatal("new key accepted before refetch was allowed")
	}
	// ...and once the limit has passed it is.
	client.nextAllowed = time.Time{}
	if _, err := v.Validate(context.Background(), newTok); err != nil {
		t.Fatalf("new key after refetch: %v", err)
	}
	if _, err := v.Validate(context.Background(), oldTok); err != nil {
		t.Errorf("old key during rotation: %v", err)
	}
}

func TestJWKSClient_RecoversWhenAuthStartsLate(t *testing.T) {
	key := newKey(t)
	kid, _ := Thumbprint(&key.PublicKey)
	srv := newJWKSServer(t, &key.PublicKey)
	srv.down.Store(true)

	client := NewJWKSClient(srv.URL)
	if err := client.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh succeeded while JWKS is down")
	}
	if err := client.Ready(context.Background()); err == nil {
		t.Fatal("Ready while JWKS is down")
	}

	srv.down.Store(false)
	time.Sleep(failureBackoff)

	// The readiness probe alone must bring the client up.
	if err := client.Ready(context.Background()); err != nil {
		t.Fatalf("Ready after auth came up: %v", err)
	}
	if _, err := client.Key(context.Background(), kid); err != nil {
		t.Errorf("Key: %v", err)
	}
}

func TestJWKSClient_UnknownKeysAreRateLimited(t *testing.T) {
	key := newKey(t)
	srv := newJWKSServer(t, &key.PublicKey)
	client := NewJWKSClient(srv.URL)
	client.Refresh(context.Background())

	for range 50 {
		client.Key(context.Background(), "made-up-kid")
	}
	if n := srv.hits.Load(); n != 1 {
		t.Errorf("JWKS fetched %d times, want 1", n)
	}
}

func TestJWKSClient_ServesCachedKeysWhenAuthIsDown(t *testing.T) {
	key := newKey(t)
	kid, _ := Thumbprint(&key.PublicKey)
	srv := newJWKSServer(t, &key.PublicKey)
	client := NewJWKSClient(srv.URL)
	client.Refresh(context.Background())

	srv.down.Store(true)
	client.cache.Load().fetchedAt = time.Time{} // stale: forces a refetch attempt
	client.nextAllowed = time.Time{}

	if _, err := client.Key(context.Background(), kid); err != nil {
		t.Errorf("cached key not served while JWKS is down: %v", err)
	}
}

// --- middleware ---

func serveAuth(t *testing.T, h http.Handler, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/test", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthenticate(t *testing.T) {
	key := newKey(t)
	signer := newSigner(t, key, testIssuer, []string{testAudience}, time.Hour)
	expired := newSigner(t, key, testIssuer, []string{testAudience}, -time.Hour)
	v := staticValidator(t, &key.PublicKey)
	tok := issue(t, signer, testIdentity)

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, found := ClaimsFromContext(r.Context())
		raw, _ := TokenFromContext(r.Context())
		if !found || claims.UserID() != "user-123" || raw != tok {
			t.Errorf("context: claims=%+v raw=%q", claims, raw)
		}
	})

	tests := []struct {
		name       string
		header     string
		wantStatus int
		wantCode   string
	}{
		{"valid", "Bearer " + tok, 200, ""},
		{"lowercase scheme", "bearer " + tok, 200, ""},
		{"missing", "", 401, "MISSING_TOKEN"},
		{"wrong scheme", "Basic dXNlcjpwYXNz", 401, "INVALID_TOKEN_FORMAT"},
		{"empty token", "Bearer ", 401, "INVALID_TOKEN_FORMAT"},
		{"expired", "Bearer " + issue(t, expired, testIdentity), 401, "INVALID_TOKEN"},
		{"garbage", "Bearer abc", 401, "INVALID_TOKEN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := ok
			if tt.wantStatus != 200 {
				handler = func(http.ResponseWriter, *http.Request) { t.Error("handler called") }
			}
			rec := serveAuth(t, Authenticate(v)(handler), tt.header)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == 401 {
				if !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
					t.Errorf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
				}
				var e middleware.APIError
				json.NewDecoder(rec.Body).Decode(&e)
				if e.Code != tt.wantCode {
					t.Errorf("code = %q, want %q", e.Code, tt.wantCode)
				}
			}
		})
	}
}

func TestRequireScopeAndRole(t *testing.T) {
	key := newKey(t)
	v := staticValidator(t, &key.PublicKey)
	tok := issue(t, newSigner(t, key, testIssuer, []string{testAudience}, time.Hour), testIdentity)
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	tests := []struct {
		name       string
		chain      http.Handler
		header     string
		wantStatus int
	}{
		{"has scope", middleware.Chain(next, Authenticate(v), RequireScope("orders:read")), "Bearer " + tok, 200},
		{"lacks scope", middleware.Chain(next, Authenticate(v), RequireScope("orders:write")), "Bearer " + tok, 403},
		{"has role", middleware.Chain(next, Authenticate(v), RequireRole("admin", "customer")), "Bearer " + tok, 200},
		{"lacks role", middleware.Chain(next, Authenticate(v), RequireRole("admin")), "Bearer " + tok, 403},
		{"scope without Authenticate", RequireScope("orders:read")(next), "Bearer " + tok, 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := serveAuth(t, tt.chain, tt.header); rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestForwardToken(t *testing.T) {
	var got string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
	}))
	defer downstream.Close()
	client := &http.Client{Transport: ForwardToken(http.DefaultTransport)}

	ctx := ContextWithClaims(context.Background(), &Claims{}, "user-token")
	req, _ := http.NewRequestWithContext(ctx, "GET", downstream.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got != "Bearer user-token" {
		t.Errorf("downstream saw %q", got)
	}
	if req.Header.Get("Authorization") != "" {
		t.Error("caller's request was modified")
	}
}

// --- hub and passwords ---

type agentFunc func(context.Context, json.RawMessage) (string, error)

func (f agentFunc) Authenticate(ctx context.Context, c json.RawMessage) (string, error) {
	return f(ctx, c)
}

func TestHub(t *testing.T) {
	hub := NewHub()
	hub.Register("apikey", agentFunc(func(_ context.Context, c json.RawMessage) (string, error) {
		if string(c) == `"secret"` {
			return "svc-1", nil
		}
		return "", ErrInvalidCredentials
	}))

	if id, err := hub.Authenticate(context.Background(), "apikey", json.RawMessage(`"secret"`)); err != nil || id != "svc-1" {
		t.Errorf("got %q, %v", id, err)
	}
	if _, err := hub.Authenticate(context.Background(), "apikey", json.RawMessage(`"wrong"`)); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := hub.Authenticate(context.Background(), "oauth", nil); !errors.Is(err, ErrUnknownMethod) {
		t.Errorf("unknown method: %v", err)
	}
}

func TestPassword(t *testing.T) {
	defer SetPasswordCostForTesting()()

	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse") || VerifyPassword(hash, "wrong horse") {
		t.Error("verify mismatch")
	}
	for _, pw := range []string{"short", strings.Repeat("x", MaxPasswordBytes+1)} {
		if _, err := HashPassword(pw); !errors.Is(err, ErrWeakPassword) {
			t.Errorf("HashPassword(%d bytes) err = %v", len(pw), err)
		}
	}
}
