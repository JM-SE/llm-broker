.PHONY: build test test-race vet fmt check clean

GO    ?= go
ARGS  ?=

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
	$(GO) clean -cache -testcache
