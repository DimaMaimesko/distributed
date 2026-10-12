package authsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"distributed/platform/auth"
	"distributed/platform/middleware"
)

const testIssuer = "https://auth.test"

func TestMain(m *testing.M) {
	restore := auth.SetPasswordCostForTesting()
	code := m.Run()
	restore()
	os.Exit(code)
}

type testEnv struct {
	srv   *httptest.Server
	store *MemoryStore
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner(key, testIssuer, []string{"orders"}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	agent, err := NewPasswordAgent(store)
	if err != nil {
		t.Fatal(err)
	}
	hub := auth.NewHub()
	hub.Register(MethodPassword, agent)

	s, err := NewServer(Config{HTTPTimeout: 5 * time.Second, RefreshTokenTTL: time.Hour}, store, hub, signer)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv, store: store}
}

func (e *testEnv) post(t *testing.T, path string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(e.srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func passwordLogin(email, password string) map[string]any {
	return map[string]any{
		"method":      MethodPassword,
		"credentials": map[string]string{"email": email, "password": password},
	}
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return v
}

func wantStatus(t *testing.T, resp *http.Response, status int, code string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d", resp.StatusCode, status)
	}
	if code != "" {
		if e := decode[middleware.APIError](t, resp); e.Code != code {
			t.Errorf("code = %q, want %q", e.Code, code)
		}
	}
}

func (e *testEnv) registerAndLogin(t *testing.T, email string) TokenPair {
	t.Helper()
	wantStatus(t, e.post(t, "/auth/register", map[string]string{"email": email, "password": "password123"}), 201, "")
	resp := e.post(t, "/auth/login", passwordLogin(email, "password123"))
	wantStatus(t, resp, 200, "")
	return decode[TokenPair](t, resp)
}

func TestRegister(t *testing.T) {
	env := newTestEnv(t)
	body := map[string]string{"email": "ann@example.com", "password": "password123"}

	resp := env.post(t, "/auth/register", body)
	wantStatus(t, resp, 201, "")
	if u := decode[userResponse](t, resp); u.Role != RoleCustomer || len(u.Scopes) == 0 {
		t.Errorf("user = %+v", u)
	}

	wantStatus(t, env.post(t, "/auth/register", map[string]string{"email": "ANN@example.com", "password": "password123"}), 409, "EMAIL_TAKEN")
	wantStatus(t, env.post(t, "/auth/register", map[string]string{"email": "bob@example.com", "password": "short"}), 400, "INVALID_FIELD")
	wantStatus(t, env.post(t, "/auth/register", map[string]string{"email": "not-an-email", "password": "password123"}), 400, "INVALID_FIELD")
}

func TestLogin_IssuesValidTokens(t *testing.T) {
	env := newTestEnv(t)
	wantStatus(t, env.post(t, "/auth/register", map[string]string{"email": "ann@example.com", "password": "password123"}), 201, "")

	resp := env.post(t, "/auth/login", passwordLogin("ann@example.com", "password123"))
	wantStatus(t, resp, 200, "")
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", resp.Header.Get("Cache-Control"))
	}
	pair := decode[TokenPair](t, resp)
	if pair.TokenType != "Bearer" || pair.ExpiresIn != 900 || pair.RefreshToken == "" {
		t.Errorf("pair = %+v", pair)
	}

	// A downstream service validates it using only the published JWKS.
	keys := auth.NewJWKSClient(env.srv.URL + "/.well-known/jwks.json")
	claims, err := auth.NewValidator(keys, testIssuer, "orders").Validate(context.Background(), pair.AccessToken)
	if err != nil {
		t.Fatalf("validate via JWKS: %v", err)
	}
	if claims.Email != "ann@example.com" || !claims.Scope.Has("orders:write") {
		t.Errorf("claims = %+v", claims)
	}
}

func TestLogin_Failures(t *testing.T) {
	env := newTestEnv(t)
	wantStatus(t, env.post(t, "/auth/register", map[string]string{"email": "ann@example.com", "password": "password123"}), 201, "")

	// Unknown user and wrong password are indistinguishable.
	wantStatus(t, env.post(t, "/auth/login", passwordLogin("ann@example.com", "wrong-password")), 401, "INVALID_CREDENTIALS")
	wantStatus(t, env.post(t, "/auth/login", passwordLogin("nobody@example.com", "password123")), 401, "INVALID_CREDENTIALS")
	wantStatus(t, env.post(t, "/auth/login", map[string]any{"method": "password", "credentials": "garbage"}), 401, "INVALID_CREDENTIALS")
	wantStatus(t, env.post(t, "/auth/login", map[string]any{"method": "ldap", "credentials": map[string]string{}}), 400, "UNKNOWN_METHOD")
}

func TestRefresh_RotatesToken(t *testing.T) {
	env := newTestEnv(t)
	first := env.registerAndLogin(t, "ann@example.com")

	resp := env.post(t, "/auth/refresh", map[string]string{"refresh_token": first.RefreshToken})
	wantStatus(t, resp, 200, "")
	second := decode[TokenPair](t, resp)
	if second.RefreshToken == first.RefreshToken || second.AccessToken == "" {
		t.Errorf("tokens not rotated: %+v", second)
	}

	// The old refresh token is single-use.
	wantStatus(t, env.post(t, "/auth/refresh", map[string]string{"refresh_token": first.RefreshToken}), 401, "INVALID_REFRESH_TOKEN")
	wantStatus(t, env.post(t, "/auth/refresh", map[string]string{"refresh_token": second.RefreshToken}), 200, "")
}

func TestRefresh_ConcurrentUseSucceedsOnce(t *testing.T) {
	env := newTestEnv(t)
	pair := env.registerAndLogin(t, "ann@example.com")
	body, _ := json.Marshal(map[string]string{"refresh_token": pair.RefreshToken})

	var wg sync.WaitGroup
	var ok atomic.Int32
	for range 20 {
		wg.Go(func() {
			resp, err := http.Post(env.srv.URL+"/auth/refresh", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ok.Add(1)
			}
		})
	}
	wg.Wait()

	if n := ok.Load(); n != 1 {
		t.Errorf("%d refreshes succeeded, want 1", n)
	}
}

func TestRefresh_ExpiredToken(t *testing.T) {
	env := newTestEnv(t)
	tok, _ := env.store.CreateRefreshToken(context.Background(), "usr_x", -time.Minute)
	wantStatus(t, env.post(t, "/auth/refresh", map[string]string{"refresh_token": tok}), 401, "INVALID_REFRESH_TOKEN")
}

func TestLogout_RevokesRefreshToken(t *testing.T) {
	env := newTestEnv(t)
	pair := env.registerAndLogin(t, "ann@example.com")

	wantStatus(t, env.post(t, "/auth/logout", map[string]string{"refresh_token": pair.RefreshToken}), 204, "")
	wantStatus(t, env.post(t, "/auth/refresh", map[string]string{"refresh_token": pair.RefreshToken}), 401, "INVALID_REFRESH_TOKEN")
}
