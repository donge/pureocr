# pureocr v2 — build, test and package.
#
#   make test          unit + integration tests (needs cgo and the host's ORT)
#   make build         CLI for the host platform -> bin/ocr
#   make bench         benchmark CLI -> bin/bench
#   make image         release binaries for linux/amd64 and linux/arm64
#                      (uses the golang image; amd64 is emulated on Apple silicon)
#   make clean
#
# The binaries embed the models and the ONNX Runtime library, so each one is a
# single self-contained file; nothing else is needed at runtime except glibc.

PLATFORMS ?= linux/amd64 linux/arm64
GO        ?= go
GOIMAGE   ?= golang:1.24-bookworm
GOPROXY   ?= https://proxy.golang.org,direct
LDFLAGS   ?= -s -w

.PHONY: test build bench image clean

test:
	CGO_ENABLED=1 $(GO) vet ./...
	CGO_ENABLED=1 $(GO) test ./...

build:
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/ocr ./example

bench:
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/bench ./bench

# Build inside the golang image (no registry round trip once it is cached).
image:
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		mkdir -p bin/$$os-$$arch; \
		echo "== $$os/$$arch"; \
		docker run --rm --platform $$p \
			-v "$(CURDIR)":/src -v "$(CURDIR)/bin/$$os-$$arch":/out \
			-w /src -e CGO_ENABLED=1 -e GOPROXY=$(GOPROXY) \
			$(GOIMAGE) bash -c 'go build -trimpath -ldflags="$(LDFLAGS)" -o /out/ocr ./example && \
			                     go build -trimpath -ldflags="$(LDFLAGS)" -o /out/bench ./bench' || exit 1; \
	done
	@ls -lh bin/*/

clean:
	rm -rf bin
