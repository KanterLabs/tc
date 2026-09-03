APP := helm
ROOT_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
WEB_DIR := $(ROOT_DIR)/web
DIST_DIR := $(ROOT_DIR)/dist
FRONTEND_DIST := $(ROOT_DIR)/internal/webassets/dist
FRONTEND_COMPAT_DIST := $(ROOT_DIR)/internal/frontend/dist
GO ?= go
NPM ?= npm
SHA ?= $(shell git rev-parse HEAD 2>/dev/null || true)

.PHONY: all web-install web-check web-test web-build openapi openapi-check frontend build test vet lint \
	docker-build compose-up compose-down bundle clean

all: build

web-install:
	cd $(WEB_DIR) && $(NPM) ci

web-check: web-install
	cd $(WEB_DIR) && $(NPM) run check

web-test: web-install
	cd $(WEB_DIR) && $(NPM) test

web-build: web-install
	cd $(WEB_DIR) && $(NPM) run build

openapi: web-install
	cd $(WEB_DIR) && $(NPM) run openapi:generate

openapi-check: web-install
	cd $(WEB_DIR) && $(NPM) run openapi:check


# Keep this order in one target so an embedded frontend can never be stale.
frontend:
	cd $(WEB_DIR) && $(NPM) ci
	cd $(WEB_DIR) && $(NPM) run openapi:check
	cd $(WEB_DIR) && $(NPM) run check
	cd $(WEB_DIR) && $(NPM) test
	cd $(WEB_DIR) && $(NPM) run build
	rm -rf $(FRONTEND_DIST) $(FRONTEND_COMPAT_DIST)
	install -d $(FRONTEND_DIST)
	cp -a $(WEB_DIR)/dist/. $(FRONTEND_DIST)/
	install -d $(FRONTEND_COMPAT_DIST)
	cp -a $(WEB_DIR)/dist/. $(FRONTEND_COMPAT_DIST)/

build: frontend
	install -d $(DIST_DIR)
	$(GO) build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/$(APP) ./cmd/$(APP)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

lint:
	@test -z "$$(gofmt -l cmd internal)" || { echo 'gofmt required' >&2; exit 1; }
	@cd $(WEB_DIR) && $(NPM) run openapi:check
	@bash -n deploy/*.sh deploy/helm-deploy-gateway
	@./deploy/test-deployment-security.sh

docker-build:
	docker build --tag $(APP):$(if $(SHA),$(SHA),local) .

compose-up:
	docker compose up --build --detach

compose-down:
	docker compose down

bundle: build
	@test -n "$(SHA)" || { echo 'bundle requires a git SHA' >&2; exit 64; }
	./deploy/build-bundle.sh $(SHA)

clean:
	rm -rf $(DIST_DIR) $(FRONTEND_DIST) $(FRONTEND_COMPAT_DIST)
