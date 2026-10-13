#!/usr/bin/env bash
# Walks through the system with curl against running services (make dev):
# register, log in, create, list and read orders, check authorization,
# update status with optimistic locking, refresh and log out. Needs only bash, curl and sed.
set -uo pipefail

: "${AUTH_PORT:=8081}"
: "${ORDERS_PORT:=8080}"
: "${ADMIN_EMAIL:=admin@example.com}"
: "${ADMIN_PASSWORD:=admin-pass-123}"

AUTH="http://localhost:${AUTH_PORT}"
ORDERS="http://localhost:${ORDERS_PORT}"
JSON='Content-Type: application/json'

bold=$'\033[1m'
reset=$'\033[0m'
step() { printf '\n%s== %s%s\n' "$bold" "$*" "$reset"; }

# field NAME: extracts a string field from a JSON object on stdin.
field() { sed -n "s/.*\"$1\":\"\([^\"]*\)\".*/\1/p"; }

# show CURL_ARGS...: runs curl, printing the status code and body.
show() {
	curl -s -w '\n-> HTTP %{http_code}\n' "$@"
}

login() {
	curl -s -H "$JSON" \
		-d "{\"method\":\"password\",\"credentials\":{\"email\":\"$1\",\"password\":\"$2\"}}" \
		"$AUTH/auth/login"
}

if ! curl -sf "$AUTH/healthz/live" >/dev/null || ! curl -sf "$ORDERS/healthz/live" >/dev/null; then
	echo "services are not running; start them with 'make dev'" >&2
	exit 1
fi

step "Health: orders is ready once NATS is connected and signing keys are loaded"
show "$ORDERS/healthz/ready"

step "Public signing keys (JWKS)"
show "$AUTH/.well-known/jwks.json"

step "Register ann and bob (409 EMAIL_TAKEN if you ran the demo before)"
show -H "$JSON" -d '{"email":"ann@example.com","password":"password123"}' "$AUTH/auth/register"
curl -s -o /dev/null -H "$JSON" -d '{"email":"bob@example.com","password":"password123"}' "$AUTH/auth/register"

step "Wrong password: same answer as an unknown email"
show -H "$JSON" -d '{"method":"password","credentials":{"email":"ann@example.com","password":"wrong-password"}}' "$AUTH/auth/login"

step "Log in as ann"
ANN_PAIR=$(login ann@example.com password123)
echo "$ANN_PAIR"
ANN=$(echo "$ANN_PAIR" | field access_token)
ANN_REFRESH=$(echo "$ANN_PAIR" | field refresh_token)
BOB=$(login bob@example.com password123 | field access_token)
ADMIN=$(login "$ADMIN_EMAIL" "$ADMIN_PASSWORD" | field access_token)

step "What is inside ann's access token (the payload is only base64, not encrypted)"
payload=$(echo "$ANN" | cut -d. -f2 | tr '_-' '/+')
while (( ${#payload} % 4 )); do payload+="="; done
echo "$payload" | base64 -d 2>/dev/null || echo "$payload" | base64 -D
echo

step "Create an order without a token"
show -H "$JSON" -d '{"product_id":"prod-001","quantity":2}' "$ORDERS/orders"

step "Create an order as ann (the consumer logs the event)"
ORDER=$(curl -s -H "$JSON" -H "Authorization: Bearer $ANN" \
	-d '{"product_id":"prod-001","quantity":2}' "$ORDERS/orders")
echo "$ORDER"
ORDER_ID=$(echo "$ORDER" | field id)

step "ann reads her order"
show -H "Authorization: Bearer $ANN" "$ORDERS/orders/$ORDER_ID"

step "bob reads ann's order: 404, so he cannot tell it exists"
show -H "Authorization: Bearer $BOB" "$ORDERS/orders/$ORDER_ID"

step "admin reads ann's order"
show -H "Authorization: Bearer $ADMIN" "$ORDERS/orders/$ORDER_ID"

step "Retry-safe create: the same Idempotency-Key twice gives one order (201, then 200 replay)"
KEY="demo-$RANDOM-$RANDOM"
for attempt in 1 2; do
	show -H "$JSON" -H "Authorization: Bearer $ANN" -H "Idempotency-Key: $KEY" \
		-d '{"product_id":"prod-002","quantity":1}' "$ORDERS/orders"
done

step "ann lists her orders, newest first"
show -H "Authorization: Bearer $ANN" "$ORDERS/orders?limit=5"

step "Admin marks ann's first order paid (version 1 -> 2)"
show -H "$JSON" -H "Authorization: Bearer $ADMIN" -X PATCH \
	-d '{"status":"paid","version":1}' "$ORDERS/orders/$ORDER_ID"

step "ann tries to cancel with the version she saw before: 409, someone changed it first"
show -H "$JSON" -H "Authorization: Bearer $ANN" -X PATCH \
	-d '{"status":"cancelled","version":1}' "$ORDERS/orders/$ORDER_ID"

step "ann re-reads (version 2) and cancels"
show -H "$JSON" -H "Authorization: Bearer $ANN" -X PATCH \
	-d '{"status":"cancelled","version":2}' "$ORDERS/orders/$ORDER_ID"

step "Status history, written in the same transaction as each change"
show -H "Authorization: Bearer $ANN" "$ORDERS/orders/$ORDER_ID/history"

step "Validation error: unknown field"
show -H "$JSON" -H "Authorization: Bearer $ANN" \
	-d '{"product_id":"prod-001","quantity":1,"priority":"high"}' "$ORDERS/orders"

step "Refresh: a new token pair; the old refresh token is now used up"
show -H "$JSON" -d "{\"refresh_token\":\"$ANN_REFRESH\"}" "$AUTH/auth/refresh"

step "Reusing the old refresh token"
show -H "$JSON" -d "{\"refresh_token\":\"$ANN_REFRESH\"}" "$AUTH/auth/refresh"

step "Log out (revokes a refresh token; access tokens live until they expire)"
NEW_REFRESH=$(login ann@example.com password123 | field refresh_token)
show -H "$JSON" -d "{\"refresh_token\":\"$NEW_REFRESH\"}" "$AUTH/auth/logout"
show -H "$JSON" -d "{\"refresh_token\":\"$NEW_REFRESH\"}" "$AUTH/auth/refresh"

echo
echo "Your access token, for your own curl requests:"
echo "export TOKEN=$ANN"
