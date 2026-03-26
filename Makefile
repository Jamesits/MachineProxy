GO ?= go
GORELEASER ?= goreleaser

.PHONY: build test clean

build:
	$(GORELEASER) build --snapshot --clean

test:
	$(GO) test ./...

clean:
	rm -rf dist
