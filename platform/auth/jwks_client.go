package auth

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// minRefreshInterval rate-limits fetches after a successful one, so a
	// flood of tokens with made-up key IDs cannot turn into a flood of
	// requests to the auth service.
	minRefreshInterval = 30 * time.Second
	// failureBackoff is the shorter wait after a failed fetch, so a
	// service that started before the auth service recovers quickly.
	failureBackoff = time.Second
	// maxKeyAge is how long fetched keys are used before refetching, so a
	// key removed from the JWKS (e.g. after a leak) stops being accepted.
	maxKeyAge = 15 * time.Minute

	maxJWKSBytes = 1 << 20
)

// JWKSClient is a KeySet backed by the auth service's JWKS endpoint.
//
// Keys are cached in memory, so validating a token normally needs no
// network call. The set is refetched when a token names an unknown key ID
// (a newly rotated-in key) or the cache is older than maxKeyAge. If a
// refetch fails, the cached keys keep being used.
type JWKSClient struct {
	url    string
	client *http.Client

	cache atomic.Pointer[keyCache]

	mu          sync.Mutex // serializes fetches
	nextAllowed time.Time  // earliest next non-forced fetch
}

type keyCache struct {
	keys      map[string]*ecdsa.PublicKey
	fetchedAt time.Time
}

func NewJWKSClient(url string) *JWKSClient {
	return &JWKSClient{url: url, client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *JWKSClient) Key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	cache := c.cache.Load()
	if cache != nil && time.Since(cache.fetchedAt) < maxKeyAge {
		if k, ok := cache.keys[kid]; ok {
			return k, nil
		}
	}

	refreshErr := c.refresh(ctx, false)

	if cache = c.cache.Load(); cache != nil {
		if k, ok := cache.keys[kid]; ok {
			return k, nil
		}
	}
	if refreshErr != nil {
		return nil, refreshErr
	}
	return nil, ErrUnknownKey
}

// Refresh fetches the key set now. Call it at startup so the first
// request does not pay for the fetch.
func (c *JWKSClient) Refresh(ctx context.Context) error {
	return c.refresh(ctx, true)
}

// Ready is a readiness check: it fails until keys have been fetched once.
// While no keys are loaded it retries the fetch itself, because an
// instance that is not ready receives no requests that would trigger one.
func (c *JWKSClient) Ready(ctx context.Context) error {
	if c.cache.Load() != nil {
		return nil
	}
	if err := c.refresh(ctx, false); err != nil {
		return err
	}
	if c.cache.Load() == nil {
		return errors.New("auth: signing keys not loaded")
	}
	return nil
}

func (c *JWKSClient) refresh(ctx context.Context, force bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Also covers callers that queued on mu while another one fetched.
	if !force && time.Now().Before(c.nextAllowed) {
		return nil
	}

	keys, err := c.fetch(ctx)
	if err != nil {
		c.nextAllowed = time.Now().Add(failureBackoff)
		return err
	}
	c.cache.Store(&keyCache{keys: keys, fetchedAt: time.Now()})
	c.nextAllowed = time.Now().Add(minRefreshInterval)
	return nil
}

func (c *JWKSClient) fetch(ctx context.Context) (map[string]*ecdsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: fetch JWKS: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: fetch JWKS: status %d", resp.StatusCode)
	}

	var set JWKS
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&set); err != nil {
		return nil, fmt.Errorf("auth: decode JWKS: %w", err)
	}

	keys := make(map[string]*ecdsa.PublicKey, len(set.Keys))
	for _, jwk := range set.Keys {
		if jwk.Use != "" && jwk.Use != "sig" {
			continue
		}
		// Skip keys we cannot use instead of failing the whole set; the
		// auth service may publish other key types in future.
		pub, err := jwk.PublicKey()
		if err != nil {
			continue
		}
		kid := jwk.KeyID
		if kid == "" {
			kid = jwk.thumbprint()
		}
		keys[kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("auth: JWKS contains no usable keys")
	}
	return keys, nil
}
