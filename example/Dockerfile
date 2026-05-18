# syntax=docker/dockerfile:1
#
# Example: build a self-contained Alpine image using pureocr.
#
# Must be built from the repository root (one level above example/):
#   docker build --build-arg ARCH=arm64 -f example/Dockerfile -t ocr .
#   docker build --build-arg ARCH=amd64 -f example/Dockerfile -t ocr .
#
# Run:
#   docker run --rm -v /path/to/images:/imgs ocr /imgs/photo.jpg

ARG ARCH=arm64

# ── Stage 1: builder ──────────────────────────────────────────────────────────
FROM --platform=linux/${ARCH} golang:1.24-bookworm AS builder
ARG ARCH

# Build tools + patchelf + glibc runtime deps (needed for patchelf to resolve symbols)
RUN apt-get update -qq && apt-get install -y -qq --no-install-recommends \
    patchelf libglib2.0-0 libpcre2-8-0 zlib1g libatomic1 \
    && rm -rf /var/lib/apt/lists/*

# Collect glibc runtime into /opt/glibc (cp -L resolves symlinks to real files)
RUN set -e; mkdir -p /opt/glibc; \
    LIBDIRS=$(find /lib /usr/lib -maxdepth 2 -name '*-linux-gnu' -type d 2>/dev/null | tr '\n' ' '); \
    for name in \
        'ld-linux*.so*' \
        libc.so.6 libm.so.6 libpthread.so.0 libdl.so.2 libgcc_s.so.1 \
        libz.so.1 libpcre2-8.so.0 libstdc++.so.6 libglib-2.0.so.0 libatomic.so.1; do \
        src=$(find $LIBDIRS -name "$name" 2>/dev/null | head -1); \
        [ -n "$src" ] && cp -L "$src" /opt/glibc/ && echo "copied $src" || echo "missing: $name"; \
    done; \
    for f in /opt/glibc/*.so*; do \
        [ -L "$f" ] && continue; \
        case "$f" in *ld-linux*) continue;; esac; \
        patchelf --set-rpath /opt/glibc "$f"; \
    done

# Copy pre-built OCR assets and patchelf RPATH → /opt/glibc
COPY assets/${ARCH}     /opt/ocr
COPY assets/ocr_model   /opt/ocr/ocr_model
RUN set -e; \
    LD=$(find /opt/glibc -name 'ld-linux*.so*' ! -type l | head -1); \
    patchelf --set-rpath /opt/glibc /opt/ocr/libmmmojo.so; \
    patchelf --set-rpath /opt/glibc /opt/ocr/libocr.so; \
    patchelf --set-rpath /opt/glibc --set-interpreter "$LD" /opt/ocr/wxocr; \
    echo "interpreter: $LD"

# Build Go binary
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY pureocr.go ./
COPY example/main.go ./example/main.go
RUN CGO_ENABLED=0 GOARCH=${ARCH} go build -o /ocr ./example

# ── Stage 2: Alpine runtime ───────────────────────────────────────────────────
FROM --platform=linux/${ARCH} alpine:latest

COPY --from=builder /opt/glibc /opt/glibc
COPY --from=builder /opt/ocr   /opt/ocr
COPY --from=builder /ocr       /ocr

# Entrypoint: run the binary under the glibc dynamic linker so that
# dlopen can load glibc-linked .so files from this musl-based host.
RUN printf '#!/bin/sh\n\
case $(uname -m) in\n\
  aarch64) LD=/opt/glibc/ld-linux-aarch64.so.1 ;;\n\
  x86_64)  LD=/opt/glibc/ld-linux-x86-64.so.2  ;;\n\
  *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;\n\
esac\n\
exec "$LD" --library-path /opt/glibc /ocr "$@"\n' \
    > /entrypoint.sh && chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
