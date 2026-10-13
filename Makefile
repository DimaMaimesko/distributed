# Run `make` or `make help` to list targets.

AUTH_PORT      ?= 8081
ORDERS_PORT    ?= 8080
DASHBOARD_PORT ?= 8090
NATS_URL       ?= nats://localhost:4222
KEY_FILE       ?= key.pem
ADMIN_EMAIL    ?= admin@example.com
ADMIN_PASSWORD ?= admin-pass-123

AUTH_URL := http://localhost:$(AUTH_PORT)
NATS_CONTAINER := distributed-nats

export AUTH_PORT ORDERS_PORT DASHBOARD_PORT NATS_URL KEY_FILE ADMIN_EMAIL ADMIN_PASSWORD

.DEFAULT_GOAL := help

##@ Quick start

.PHONY: dev
dev: ## Run NATS, auth, orders, consumer and dashboard (Ctrl-C stops all)
	./scripts/dev.sh

.PHONY: open
open: ## Open the status dashboard in the browser
	open http://localhost:$(DASHBOARD_PORT) 2>/dev/null || xdg-open http://localhost:$(DASHBOARD_PORT)

.PHONY: demo
demo: ## Walk through the API with curl (needs `make dev` running)
	./scripts/demo.sh

##@ Infrastructure

.PHONY: nats
nats: ## Start NATS in Docker on :4222, with monitoring on :8222
	docker run -d --rm --name $(NATS_CONTAINER) -p 4222:4222 -p 8222:8222 nats:latest -m 8222

.PHONY: nats-stop
nats-stop: ## Stop the NATS container
	docker stop $(NATS_CONTAINER)

$(KEY_FILE):
	openssl ecparam -name prime256v1 -genkey -noout -out $(KEY_FILE)

.PHONY: key
key: $(KEY_FILE) ## Create the ES256 signing key (key.pem) if missing

##@ Run one service (each in its own terminal)

.PHONY: run-auth
run-auth: $(KEY_FILE) ## Auth service on :8081
	PORT=$(AUTH_PORT) AUTH_ISSUER=$(AUTH_URL) AUTH_PRIVATE_KEY_FILE=$(KEY_FILE) \
	ADMIN_EMAIL=$(ADMIN_EMAIL) ADMIN_PASSWORD=$(ADMIN_PASSWORD) \
	go run ./cmd/auth

.PHONY: run-orders
run-orders: ## Orders service on :8080
	PORT=$(ORDERS_PORT) NATS_URL=$(NATS_URL) \
	AUTH_ISSUER=$(AUTH_URL) AUTH_JWKS_URL=$(AUTH_URL)/.well-known/jwks.json \
	go run ./cmd/orders

.PHONY: run-consumer
run-consumer: ## Consumer that logs orders.created events
	NATS_URL=$(NATS_URL) go run ./cmd/consumer

.PHONY: run-consumers
run-consumers: ## Two consumers in one queue group (each event goes to one)
	NATS_URL=$(NATS_URL) QUEUE_GROUP=workers go run ./cmd/consumer & \
	NATS_URL=$(NATS_URL) QUEUE_GROUP=workers go run ./cmd/consumer & \
	wait

.PHONY: run-dashboard
run-dashboard: ## Status dashboard on :8090
	PORT=$(DASHBOARD_PORT) NATS_URL=$(NATS_URL) \
	AUTH_URL=$(AUTH_URL) ORDERS_URL=http://localhost:$(ORDERS_PORT) \
	go run ./cmd/dashboard

.PHONY: run-producer
run-producer: ## Chapter 3 demo producer: an order event every 2s
	NATS_URL=$(NATS_URL) go run ./cmd/producer

.PHONY: run-pool-demo
run-pool-demo: ## Chapter 2 worker pool and graceful shutdown demo (:8080)
	go run .

##@ Development

.PHONY: build
build: ## Build all binaries into bin/
	go build -o bin/ ./cmd/...

.PHONY: test
test: ## Run all tests with the race detector
	go test -race ./...

.PHONY: cover
cover: ## Run tests and open the coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

.PHONY: check
check: ## gofmt, go vet and tests: run before committing
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go test -race ./...

.PHONY: fmt
fmt: ## Format all Go files
	gofmt -w .

.PHONY: tidy
tidy: ## Tidy go.mod and go.sum
	go mod tidy

.PHONY: clean
clean: ## Remove build and coverage output (keeps key.pem)
	rm -rf bin coverage.out

.PHONY: help
help: ## Show this help
	@awk 'BEGIN { FS = ":.*##"; printf "Usage: make \033[36m<target>\033[0m\n" } \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
