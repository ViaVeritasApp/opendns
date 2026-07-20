# opendns — build & run helpers (native Go; no Docker).
# For containerised deployment see docker-compose.yml and the README.

GO   ?= go
BIN  ?= bin/opendns
PKG  := ./cmd/opendns

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-8s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the static binary natively to $(BIN)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o $(BIN) $(PKG)

.PHONY: run
run: build ## Build and start opendns natively (reads config from the environment)
	./$(BIN)

.PHONY: test
test: ## Run the test suite
	$(GO) test -race ./...

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: tidy
tidy: ## Sync go.mod / go.sum
	$(GO) mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin

.PHONY: token
token: ## Generate a random admin token (set as OPENDNS_ADMIN_TOKEN)
	@openssl rand -hex 32

.PHONY: health
health: ## Hit the admin liveness endpoint of a running instance (expects: ok)
	curl -fsS localhost:8080/healthz
