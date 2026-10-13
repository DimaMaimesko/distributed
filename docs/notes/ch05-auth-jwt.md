# Chapter 5: Authentication and JWT across services

## The idea
In a distributed system every request is from a stranger. A signed token (JWT) lets any service verify the caller locally, in microseconds, without calling the auth service.

## Key points
- **A JWT** is `header.payload.signature`, base64url-encoded. The payload is readable by anyone; it's signed, not encrypted.
- **The signing algorithm decides who can forge tokens:**
  - HS256 uses a shared secret, so every verifier can also forge.
  - RS256 and ES256 are asymmetric: only auth holds the private key, and verifiers hold public keys that can't sign.
  - **Use ES256** for new systems: P-256, small, fast.
- **JWKS** (`/.well-known/jwks.json`) publishes public keys. Services cache them and refetch when they see an unknown `kid`. That makes **key rotation** possible without downtime: publish the new key, start signing with it, and remove the old one after the token lifetime has passed.
- **Validate everything:** the algorithm against an allow-list (blocks `alg: none` and HS256 keyed with the public key), signature, `exp`, `iss`, and `aud`.
- **Access tokens are short-lived (15 min) and can't be revoked. Refresh tokens are opaque, stored as hashes, and single-use.** Each refresh atomically swaps the old token for a new one, so a replayed refresh token fails.
- **Scopes** (`orders:read`) are fine-grained permissions; **roles** (`admin`) are coarse groupings. Check whichever fits the route.
- **Token propagation:** forward the user's token for calls made on behalf of the user. Background jobs use their own service tokens, never a user's.
- **Inside a private VPC you still need user identity.** The network says who can connect, not who the user is. A plain `X-User-ID` header can be forged by anything inside the network.

## Rules of thumb
- Give the same answer for an unknown email and a wrong password, **and the same timing** (run a dummy bcrypt check).
- bcrypt at cost 12 is about 300ms, which is fine for login. Never use it on hot paths.
- Return 404, not 403, for other users' resources, so you don't reveal that they exist.
- Don't log every invalid token. Do log a valid signature with the wrong `iss` or `aud`: it means a token is leaking between environments.
- Don't put a user's JWT inside a queued event. It may expire before a consumer handles it. Put the user ID in the event.

## Traps (bugs in the book's code)
- **`key.X.Bytes()` drops leading zero bytes**, so about 1 key in 128 produces an invalid JWK. Use fixed-width encoding (`ecdsa.PublicKey.Bytes()`).
- **No `iss` or `aud` check** in the book means tokens from staging, or meant for another service, are accepted.
- **Key IDs by position (`key-%d`) and caching a single key** break rotation. Use the RFC 7638 thumbprint as `kid` and cache by `kid`.
- **The refresh handler ignored errors** (`user, _ :=`, `token, _ :=`).
- **A rate-limited JWKS refetch caused a deadlock at startup.** If auth starts after orders, readiness never recovers: no traffic arrives, so nothing triggers a refetch. Retry quickly after a failure, and let readiness itself retry the fetch.
- **A modified token string was accepted.** The last base64 character of the signature has unused bits, so one token can be written several ways. Use strict decoding so a blocklist keyed on the token string works.

## In this repo
`platform/auth` (token, JWKS, middleware, hub, passwords), `internal/authsvc`, `cmd/auth`.

## Check yourself
1. Why can't a service holding only the public key forge a token?
2. Walk through a zero-downtime key rotation.
3. Why are refresh tokens opaque strings and not JWTs?
4. What's the "alg: none" attack, and which two checks stop it?
