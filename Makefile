.PHONY: build test test-race vet fmt fmt-check scan check clean release checksums image

GO      ?= go
ARGS    ?=
VERSION ?= 0.1.0
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILDTIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/JM-SE/llm-broker/internal/version.Version=$(VERSION) \
	-X github.com/JM-SE/llm-broker/internal/version.Commit=$(COMMIT) \
	-X github.com/JM-SE/llm-broker/internal/version.BuildTime=$(BUILDTIME)

build:
	$(GO) build $(ARGS) ./...

test:
	$(GO) test -count=1 -p=1 $(ARGS) ./...

test-race:
	$(GO) test -count=1 -race -p=1 $(ARGS) ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

fmt-check:
	@files="$$(gofmt -l $$(git ls-files '*.go' --cached --others --exclude-standard))"; \
	if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

scan:
	./scripts/check-h00-boundaries.sh


# The acceptance check is intentionally non-skipping: callers must provide the
# three disposable PostgreSQL role URLs and the harnesses must execute.
check: fmt-check vet scan
	LLM_BROKER_REQUIRE_TEST_DATABASE=1 $(GO) test -count=1 -race -p=1 $(ARGS) ./...
	$(GO) build ./...
	$(GO) mod verify
	git diff --check

clean:
	rm -f llm-broker
	rm -rf dist
	$(GO) clean -cache -testcache

# Release artifacts for the supported matrix (linux/amd64, linux/arm64).
# Produces dist/llm-broker-<version>-<goos>-<goarch>.tar.gz plus checksums.
release:
	@mkdir -p dist
	@cp LICENSE dist/LICENSE
	@for target in "linux amd64" "linux arm64"; do \
		set -- $$target; \
		echo "building $$1/$$2"; \
		GOOS=$$1 GOARCH=$$2 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/llm-broker-$(VERSION)-$$1-$$2 ./cmd/llm-broker; \
		tar -C dist -czf dist/llm-broker-$(VERSION)-$$1-$$2.tar.gz \
			llm-broker-$(VERSION)-$$1-$$2 LICENSE; \
		rm dist/llm-broker-$(VERSION)-$$1-$$2; \
	done
	$(MAKE) checksums

checksums:
	cd dist && sha256sum llm-broker-*.tar.gz > SHA256SUMS

# Container image with injected version metadata (non-root).
image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) \
		--build-arg BUILDTIME=$(BUILDTIME) -t llm-broker:$(VERSION) .
