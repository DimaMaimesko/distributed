#!/usr/bin/env bash
# Runs the whole system locally: NATS (in Docker, if not already running),
# the auth service, the orders service, the event consumer and the status
# dashboard. Logs from
# every process are prefixed with its name. Ctrl-C stops everything.
set -euo pipefail

cd "$(dirname "$0")/.."

: "${AUTH_PORT:=8081}"
: "${ORDERS_PORT:=8080}"
: "${DASHBOARD_PORT:=8090}"
: "${NATS_URL:=nats://localhost:4222}"
: "${KEY_FILE:=key.pem}"
: "${ADMIN_EMAIL:=admin@example.com}"
: "${ADMIN_PASSWORD:=admin-pass-123}"

AUTH_URL="http://localhost:${AUTH_PORT}"

if ! nc -z localhost 4222 2>/dev/null; then
	echo "starting NATS in Docker..."
	docker run -d --rm --name distributed-nats -p 4222:4222 -p 8222:8222 nats:latest -m 8222 >/dev/null
	sleep 1
elif ! nc -z localhost 8222 2>/dev/null; then
	echo "warning: NATS is running without monitoring on :8222, so the dashboard cannot show it."
	echo "         Stop it and rerun, or start NATS with: make nats"
fi

if [[ ! -f "$KEY_FILE" ]]; then
	echo "creating signing key $KEY_FILE..."
	openssl ecparam -name prime256v1 -genkey -noout -out "$KEY_FILE"
fi

echo "building..."
go build -o bin/ ./cmd/...

pids=()
cleanup() {
	trap - INT TERM EXIT
	echo
	echo "stopping..."
	kill -INT "${pids[@]}" 2>/dev/null || true
	wait 2>/dev/null || true
	exit 0
}
trap cleanup INT TERM EXIT

# run NAME CMD... starts CMD in the background with prefixed output.
run() {
	local name=$1
	shift
	"$@" > >(awk -v p="$(printf '[%-8s]' "$name")" '{ print p, $0; fflush() }') 2>&1 &
	pids+=($!)
}

run auth env PORT="$AUTH_PORT" AUTH_ISSUER="$AUTH_URL" \
	AUTH_PRIVATE_KEY_FILE="$KEY_FILE" \
	ADMIN_EMAIL="$ADMIN_EMAIL" ADMIN_PASSWORD="$ADMIN_PASSWORD" \
	./bin/auth

run orders env PORT="$ORDERS_PORT" NATS_URL="$NATS_URL" \
	AUTH_ISSUER="$AUTH_URL" AUTH_JWKS_URL="$AUTH_URL/.well-known/jwks.json" \
	./bin/orders

run consumer env NATS_URL="$NATS_URL" ./bin/consumer

run dashboard env PORT="$DASHBOARD_PORT" NATS_URL="$NATS_URL" \
	AUTH_URL="$AUTH_URL" ORDERS_URL="http://localhost:${ORDERS_PORT}" \
	./bin/dashboard

echo "auth on :$AUTH_PORT, orders on :$ORDERS_PORT"
echo "dashboard: http://localhost:$DASHBOARD_PORT"
echo "Run 'make demo' in another terminal. Ctrl-C to stop."
wait
