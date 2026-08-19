# Every target here mirrors what CI runs, so a green `make ci` locally means a
# green pipeline.

# Tool credentials live in .env, which is not committed. Only the two Sonar
# variables are exported, to keep the rest of the file out of every command's
# environment.
-include .env
export SONAR_HOST_URL SONAR_TOKEN

BINARY      := bin/aegisd
COVERAGE    := coverage.out
GOSEC       := github.com/securego/gosec/v2/cmd/gosec@v2.28.0
SCANNER     := sonarsource/sonar-scanner-cli:12.1

SONAR_HOST_URL ?=
SONAR_TOKEN    ?=

# Derived from go.mod so the toolchain has a single source of truth, and
# injected into every Dockerfile. The ARG defaults in those files are only a
# fallback for building them without make.
GO_VERSION := $(shell awk '/^go /{print $$2}' go.mod)

IMAGE     := aegis
# Only the semantic version needs injecting; the revision and the build time are
# stamped by the toolchain from the repository.
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
PLATFORMS := linux/amd64,linux/arm64

.DEFAULT_GOAL := help

##@ General

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: run
run: ## Run the server from source
	go run ./cmd/server

.PHONY: build
build: ## Compile the server binary
	go build -o $(BINARY) ./cmd/server

.PHONY: tidy
tidy: ## Sync go.mod and go.sum
	go mod tidy

.PHONY: fmt
fmt: ## Format the code
	go fmt ./...

.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf bin $(COVERAGE) coverage.html

##@ Checks

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt'd
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet, including the integration tagged files
	go vet ./...
	go vet -tags=integration ./test/...

.PHONY: test
test: ## Run the unit tests with the race detector
	go test -race -covermode=atomic -coverprofile=$(COVERAGE) ./...

.PHONY: test-integration
test-integration: ## Run the integration tests against the compiled binary
	go test -tags=integration -timeout=5m ./test/integration/...

.PHONY: cover
cover: test ## Show coverage per function
	go tool cover -func=$(COVERAGE)

.PHONY: cover-html
cover-html: test ## Open the coverage report in the browser
	go tool cover -html=$(COVERAGE) -o coverage.html
	@echo "wrote coverage.html"

.PHONY: gosec
gosec: ## Run the security scanner
	go run $(GOSEC) -fmt=text -exclude-dir=.github ./...

.PHONY: ci
ci: fmt-check vet test test-integration gosec ## Run everything the pipeline runs

##@ Sonar

.PHONY: sonar-check
sonar-check:
	@if [ -z "$(SONAR_HOST_URL)" ] || [ -z "$(SONAR_TOKEN)" ]; then \
		echo "SONAR_HOST_URL and SONAR_TOKEN are required."; \
		echo "Set them in .env or pass them: make sonar-scan SONAR_HOST_URL=... SONAR_TOKEN=..."; \
		exit 1; \
	fi

# Checked before the tests so a missing setting fails immediately instead of
# after a full run.
.PHONY: sonar-scan
sonar-scan: sonar-check test ## Analyse the project on the configured SonarQube
	docker run --rm \
		-v "$(CURDIR):/usr/src" \
		-e SONAR_HOST_URL=$(SONAR_HOST_URL) \
		-e SONAR_TOKEN=$(SONAR_TOKEN) \
		$(SCANNER)

##@ Docker

.PHONY: image
image: ## Build the production image for the current architecture
	docker build -f docker/Dockerfile.production --target production \
		--build-arg GO_VERSION=$(GO_VERSION) --build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

.PHONY: image-debug
image-debug: ## Build the production image on a variant that carries a shell
	docker build -f docker/Dockerfile.production --target debug \
		--build-arg GO_VERSION=$(GO_VERSION) --build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION)-debug .

.PHONY: image-multiarch
image-multiarch: ## Cross build the production image for every target platform
	docker buildx build -f docker/Dockerfile.production --platform $(PLATFORMS) --target production \
		--build-arg GO_VERSION=$(GO_VERSION) --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

.PHONY: image-run
image-run: image ## Run the production image locally
	docker run --rm -p 7500:7500 -e HTTP_SERVER_HOST=0.0.0.0 $(IMAGE):latest

##@ Development environment

.PHONY: dev
dev: ## Start the development server with hot reload and delve on 2345
	docker compose up --build

.PHONY: dev-down
dev-down: ## Stop the development environment
	docker compose down

.PHONY: dev-logs
dev-logs: ## Follow the development server logs
	docker compose logs -f aegis

##@ Kubernetes

.PHONY: tilt
tilt: ## Start the Tilt loop against the local cluster
	tilt up

.PHONY: tilt-ci
tilt-ci: ## Deploy once, wait until healthy and exit
	tilt ci

.PHONY: tilt-down
tilt-down: ## Remove everything Tilt deployed
	tilt down

# Scoped to this project's images on purpose. A blanket `docker image prune`
# or `builder prune` would also drop layers and caches belonging to everything
# else on the machine.
.PHONY: tilt-clean
tilt-clean: ## Drop images left behind by previous Tilt sessions
	@images=$$(docker images --filter 'reference=$(IMAGE):tilt-*' -q | sort -u); \
	if [ -z "$$images" ]; then \
		echo "no leftover images"; \
	else \
		echo "$$images" | xargs docker rmi -f >/dev/null 2>&1 || true; \
		echo "removed $$(echo "$$images" | wc -l | tr -d ' ') image(s)"; \
	fi

.PHONY: k8s-manifests
k8s-manifests: ## Render the production manifests
	kubectl kustomize deploy/k8s/base
