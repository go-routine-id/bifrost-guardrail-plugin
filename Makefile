# Makefile — build & maintain a custom Bifrost image with the built-in guardrail plugin.
#
# The official maximhq/bifrost image is statically linked, so Go .so plugins cannot be
# loaded (`plugin.Open` -> "Dynamic loading not supported"). The working approach is to
# compile the guardrail into the binary as a built-in plugin and ship a custom image.
#
# Our changes on top of upstream maximhq/bifrost:
#   - guardrail.go ........ the plugin source (a built-in HTTPTransportPlugin)
#   - wiring.patch ........ 2 small edits to upstream files (built-in registry wiring)
#
# When upstream ships a new release:
#   make update TAG=transports/v1.6.1
#
# That fetches the new tag, re-applies the guardrail on top, vets, and rebuilds the image
# on the build host. Nothing is deployed automatically.

# ---- Configuration (override on the command line) ----
FORK_DIR     ?= ./bifrost                 # local clone of github.com/maximhq/bifrost
TAG          ?= transports/v1.6.0         # upstream tag to build from
SERVER       ?= dev@your-build-host       # host with native docker (amd64) to build on
IMAGE_REPO   ?= your-org/bifrost

# Image version derived from TAG: "transports/v1.6.0" -> "1.6.0-guardrail"
VERSION      := $(patsubst transports/v%,%,$(TAG))-guardrail
IMAGE        := $(IMAGE_REPO):$(VERSION)

ROOT          := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
SRC           := $(ROOT)/guardrail.go
PATCH         := $(ROOT)/wiring.patch
GUARDRAIL_PKG := transports/bifrost-http/guardrail

REMOTE_CTX   := /tmp/bifrost-guardrail-build
CTX_TAR      := /tmp/bifrost-guardrail-ctx.tar.gz

.DEFAULT_GOAL := help

.PHONY: update
update: prepare vet build ## Re-apply guardrail onto TAG and rebuild the image
	@echo ""
	@echo "==> Done. Image built on $(SERVER): $(IMAGE)"

.PHONY: build
build: context upload image ## Build the image on SERVER from the current fork state
	@echo "==> Image ready: $(IMAGE)"

.PHONY: prepare
prepare: ## Checkout TAG in the fork and re-apply guardrail.go + wiring.patch
	@test -d "$(FORK_DIR)/.git" || { echo "ERROR: FORK_DIR ($(FORK_DIR)) is not a clone of maximhq/bifrost"; exit 1; }
	@echo "==> Fetching tags and checking out $(TAG)"
	cd "$(FORK_DIR)" && git fetch --tags --quiet && git checkout -f "$(TAG)"
	@echo "==> Installing guardrail.go into the fork"
	mkdir -p "$(FORK_DIR)/$(GUARDRAIL_PKG)"
	cp "$(SRC)" "$(FORK_DIR)/$(GUARDRAIL_PKG)/guardrail.go"
	@echo "==> Applying wiring.patch"
	@cd "$(FORK_DIR)" && if git apply --check "$(PATCH)" 2>/dev/null; then \
		git apply "$(PATCH)" && echo "    patch applied cleanly"; \
	elif git apply --3way --check "$(PATCH)" 2>/dev/null; then \
		git apply --3way "$(PATCH)" && echo "    patch applied via 3-way merge"; \
	else \
		echo "ERROR: wiring.patch did not apply on $(TAG)."; \
		echo "       Upstream likely changed the plugin registry. Re-wire by hand, then: make regen-patch"; \
		exit 1; \
	fi

.PHONY: regen-patch
regen-patch: ## Regenerate wiring.patch + guardrail.go from the current fork state
	cd "$(FORK_DIR)" && git diff -- transports/bifrost-http/server/plugins.go transports/bifrost-http/lib/config.go > "$(PATCH)"
	cp "$(FORK_DIR)/$(GUARDRAIL_PKG)/guardrail.go" "$(SRC)"
	@echo "==> Updated $(PATCH) and $(SRC)"

.PHONY: vet
vet: ## go vet the guardrail + wiring packages locally
	cd "$(FORK_DIR)/transports" && go vet ./bifrost-http/guardrail/ ./bifrost-http/server/ ./bifrost-http/lib/

.PHONY: context
context: ## Pack the docker build context (ui + transports) into a tarball
	cd "$(FORK_DIR)" && tar --exclude='node_modules' --exclude='.git' --exclude='*/dist' \
		--exclude='*/out' --exclude='.DS_Store' -czf "$(CTX_TAR)" .dockerignore ui transports
	@ls -lh "$(CTX_TAR)"

.PHONY: upload
upload: ## Upload + extract the build context on SERVER
	scp -o ConnectTimeout=15 "$(CTX_TAR)" "$(SERVER):$(CTX_TAR)"
	ssh "$(SERVER)" 'rm -rf $(REMOTE_CTX) && mkdir -p $(REMOTE_CTX) && tar xzf $(CTX_TAR) -C $(REMOTE_CTX) 2>/dev/null; echo extracted'

.PHONY: image
image: ## Build the docker image on SERVER (native, no registry)
	ssh "$(SERVER)" 'cd $(REMOTE_CTX) && docker build -f transports/Dockerfile -t $(IMAGE) --build-arg VERSION=$(VERSION) .'
	ssh "$(SERVER)" 'docker images | grep "$(VERSION)"'

.PHONY: smoke
smoke: ## Run a throwaway container to confirm guardrail registers + blocks (port 18889)
	ssh "$(SERVER)" 'rm -rf /tmp/gr-smoke && mkdir -p /tmp/gr-smoke && chmod 777 /tmp/gr-smoke && \
		printf "%s" "$$GR_SMOKE_CONFIG" > /tmp/gr-smoke/config.json && \
		docker rm -f gr-smoke >/dev/null 2>&1; \
		docker run -d --name gr-smoke -p 127.0.0.1:18889:18889 -e APP_PORT=18889 -e APP_HOST=0.0.0.0 \
			-v /tmp/gr-smoke:/app/data $(IMAGE) >/dev/null && \
		until curl -s -o /dev/null http://127.0.0.1:18889/health; do sleep 2; done; \
		echo -n "health: "; curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:18889/health; \
		docker logs gr-smoke 2>&1 | grep -i "guardrail - active" || echo "WARN: guardrail not active"; \
		echo -n "block: "; curl -s -o /dev/null -w "%{http_code}\n" -X POST http://127.0.0.1:18889/anthropic/v1/messages -H "Content-Type: application/json" -d "{\"model\":\"x\",\"max_tokens\":10,\"messages\":[{\"role\":\"user\",\"content\":\"blokir-tes-guardrail\"}]}"; \
		docker rm -f gr-smoke >/dev/null && rm -rf /tmp/gr-smoke && echo "smoke cleaned"'
export GR_SMOKE_CONFIG
GR_SMOKE_CONFIG := {"$$schema":"https://www.getbifrost.ai/schema","client":{"drop_excess_requests":false},"providers":{},"plugins":[{"name":"guardrail","enabled":true,"config":{"system_prompt":"smoke","system_mode":"prepend","block_patterns":["(?i)blokir-tes-guardrail"],"block_message":"blocked"}}],"config_store":{"enabled":true,"type":"sqlite","config":{"path":"/app/data/config.db"}},"logs_store":{"enabled":true,"type":"sqlite","config":{"path":"/app/data/logs.db"}}}

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n",$$1,$$2}'
