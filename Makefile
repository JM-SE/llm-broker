.PHONY: build test test-race vet fmt check clean release checksums image

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
	$(GO) build $(ARGS) ./cmd/llm-broker

test:
	$(GO) test $(ARGS) ./...

test-race:
	$(GO) test -race $(ARGS) ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

check: fmt test-race vet
	$(GO) build ./cmd/llm-broker

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
