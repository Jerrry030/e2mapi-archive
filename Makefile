SHELL := /bin/sh

GO_MODULES := app/e2m-core app/e2m-agent packages/e2m-contracts
WEB_DIR := web/console
DIST_DIR ?= dist
AGENT_IMAGE ?= e2m-agent:local

.PHONY: help fmt fmt-check lint test test-race build ci security-scan go-fmt go-fmt-check go-lint go-test go-test-race web-install web-lint web-format web-format-check web-test web-build docker-build agent-build agent-image agent-image-smoke dev-core dev-console

help:
	@echo "E2M engineering commands"
	@echo "  make fmt            Format Go + web code"
	@echo "  make fmt-check      Check Go + web formatting"
	@echo "  make lint           Run Go vet + web ESLint"
	@echo "  make test           Run Go tests"
	@echo "  make test-race      Run Go tests with the race detector"
	@echo "  make web-test       Run web unit tests"
	@echo "  make build          Build web console + Core and Agent images"
	@echo "  make agent-build    Build the e2m-agent binary"
	@echo "  make agent-image    Build the e2m-agent container image"
	@echo "  make agent-image-smoke  Verify the Agent image user/data directory"
	@echo "  make ci             Run local CI gate"
	@echo "  make security-scan  Run local vulnerability/audit checks"

fmt: go-fmt web-format

fmt-check: go-fmt-check web-format-check

lint: go-lint web-lint

test: go-test

test-race: go-test-race

build: web-build docker-build agent-image

ci: fmt-check lint test web-test web-build

go-fmt:
	gofmt -w $$(find app packages -name '*.go' -type f)

go-fmt-check:
	@files=$$(gofmt -l $$(find app packages -name '*.go' -type f)); \
	if [ -n "$$files" ]; then echo "gofmt needed:"; echo "$$files"; exit 1; fi

go-lint:
	@for module in $(GO_MODULES); do \
		echo "==> go vet $$module"; \
		(cd $$module && go vet ./...); \
	done

go-test:
	@for module in $(GO_MODULES); do \
		echo "==> go test $$module"; \
		(cd $$module && go test ./...); \
	done

go-test-race:
	@for module in $(GO_MODULES); do \
		echo "==> go test -race $$module"; \
		(cd $$module && go test -race ./...); \
	done

web-install:
	npm ci --prefix $(WEB_DIR)

web-lint:
	npm run lint --prefix $(WEB_DIR)

web-format:
	npm run format --prefix $(WEB_DIR)

web-format-check:
	npm run format:check --prefix $(WEB_DIR)

web-test:
	npm run test --prefix $(WEB_DIR)

web-build:
	npm run build --prefix $(WEB_DIR)

docker-build:
	docker build -f app/e2m-core/Dockerfile -t e2m-core:local .

agent-build:
	mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(DIST_DIR)/e2m-agent ./app/e2m-agent/cmd/e2m-agent

agent-image:
	docker build -f app/e2m-agent/Dockerfile -t $(AGENT_IMAGE) .

agent-image-smoke: agent-image
	docker run --rm --entrypoint /bin/sh $(AGENT_IMAGE) -ec 'test "$$(id -u)" -ne 0; test -w /var/lib/e2m-agent; touch /var/lib/e2m-agent/.image-smoke'

security-scan:
	@for module in $(GO_MODULES); do \
		echo "==> govulncheck $$module"; \
		(cd $$module && govulncheck ./...); \
	done
	npm audit --audit-level=high --registry=https://registry.npmjs.org/
	npm audit --audit-level=high --registry=https://registry.npmjs.org/ --prefix $(WEB_DIR)

dev-core:
	go run ./app/e2m-core/cmd/e2m-core

dev-console:
	npm run dev --prefix $(WEB_DIR)
