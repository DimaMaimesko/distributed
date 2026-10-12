package auth

import "net/http"

// ForwardToken wraps an http.RoundTripper so that outgoing requests carry
// the caller's bearer token from the request context. The downstream
// service validates it itself and sees the same user.
//
// Use it only for calls made on behalf of the user. Background work must
// use its own service credentials, never a user's token.
//
//	client := &http.Client{Transport: auth.ForwardToken(http.DefaultTransport)}
//	req, _ := http.NewRequestWithContext(r.Context(), "POST", url, body)
//	client.Do(req)
func ForwardToken(next http.RoundTripper) http.RoundTripper {
	return forwardToken{next: next}
}

type forwardToken struct {
	next http.RoundTripper
}

func (f forwardToken) RoundTrip(req *http.Request) (*http.Response, error) {
	token, ok := TokenFromContext(req.Context())
	if !ok || req.Header.Get("Authorization") != "" {
		return f.next.RoundTrip(req)
	}
	// A RoundTripper must not modify the caller's request.
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token)
	return f.next.RoundTrip(req)
}
