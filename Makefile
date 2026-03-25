GO ?= go
GORELEASER ?= goreleaser

.PHONY: build test clean

build:
	$(GORELEASER) build --snapshot --clean
	$(MAKE) -C hook all

test:
	$(GO) test ./...
	$(MAKE) -C hook test

clean:
	rm -rf dist
	$(MAKE) -C hook clean
