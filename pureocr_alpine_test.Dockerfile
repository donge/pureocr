# pureocr_alpine_test.Dockerfile
#
# Stage 1 (debian/glibc builder):
#   - Collect glibc runtime libs → /opt/glibc
#   - Compile integration test binary (embeds ocr_helper + all OCR assets)
#
# Stage 2 (Alpine runtime):
#   - Install poppler / tesseract (tequila deps, not actually needed for OCR test)
#   - Copy /opt/glibc
#   - Copy test binary → run
#
# Build & run (on ARM server):
#   docker build -f ~/pureocr_alpine_test.Dockerfile -t pureocr_alpine_test ~/pureocr
#   docker run --rm -e TEST_IMG=/img/test.jpg -v /path/to/img:/img pureocr_alpine_test

# ── Stage 1: glibc builder ────────────────────────────────────────────────────
FROM docker.servicewall.cn/golang-builder:1.26.3 AS builder

RUN apt-get update -qq && apt-get install -y -qq \
    libglib2.0-0t64 libpcre2-8-0 zlib1g patchelf \
    && rm -rf /var/lib/apt/lists/*

# Collect all glibc/support libs that wxocr and libmmmojo.so need at runtime.
# Use explicit paths (ldd is unreliable in this container config).
RUN mkdir -p /opt/glibc && \
    for f in \
        /lib/aarch64-linux-gnu/ld-linux-aarch64.so.1 \
        /lib/aarch64-linux-gnu/libc.so.6 \
        /lib/aarch64-linux-gnu/libm.so.6 \
        /lib/aarch64-linux-gnu/libpthread.so.0 \
        /lib/aarch64-linux-gnu/libgcc_s.so.1 \
        /lib/aarch64-linux-gnu/libz.so.1 \
        /lib/aarch64-linux-gnu/libpcre2-8.so.0 \
        /usr/lib/aarch64-linux-gnu/libdl.so.2 \
        /usr/lib/aarch64-linux-gnu/libstdc++.so.6 \
        /usr/lib/aarch64-linux-gnu/libatomic.so.1 \
        /usr/lib/aarch64-linux-gnu/libglib-2.0.so.0 \
    ; do [ -f "$f" ] && cp "$f" /opt/glibc/ || true; done && \
    # Fallback: also try /lib path for libdl and libstdc++
    for f in /lib/aarch64-linux-gnu/libdl.so.2 /lib/aarch64-linux-gnu/libstdc++.so.6; do \
        [ -f "$f" ] && cp "$f" /opt/glibc/ 2>/dev/null || true; \
    done && \
    # Patchelf every .so (except dynamic linker) to use /opt/glibc rpath
    for f in /opt/glibc/*.so*; do \
        case "$f" in *ld-linux*) continue ;; esac ; \
        patchelf --set-rpath /opt/glibc "$f" 2>/dev/null || true ; \
    done && \
    ls -la /opt/glibc/

WORKDIR /src
COPY . .

# Patchelf the embedded OCR assets so they resolve deps from /opt/glibc at runtime.
# This modifies the files in-place before they get compiled into the test binary.
RUN patchelf --set-rpath /opt/glibc embed/linux_arm64/libpureocr.so && \
    patchelf --set-rpath /opt/glibc \
             --set-interpreter /opt/glibc/ld-linux-aarch64.so.1 \
             embed/linux_arm64/wxocr && \
    patchelf --set-rpath /opt/glibc \
             --set-interpreter /opt/glibc/ld-linux-aarch64.so.1 \
             embed/linux_arm64/ocr_helper

RUN go mod download

# Build integration test binary.
# embed/linux_arm64/ocr_helper must already exist (pre-built + patchelf'd).
RUN CGO_ENABLED=0 go test -buildvcs=false -c -tags integration -o /out/pureocr_test .

# ── Stage 2: Alpine runtime ───────────────────────────────────────────────────
FROM alpine_gcompat_test AS runtime

# /opt/glibc: private glibc for ocr_helper subprocess
COPY --from=builder /opt/glibc /opt/glibc

# Test binary (CGO_ENABLED=0 musl-compatible, embeds ocr_helper + OCR assets)
COPY --from=builder /out/pureocr_test /usr/local/bin/pureocr_test

ENTRYPOINT ["/usr/local/bin/pureocr_test", "-test.v", "-test.run", "TestOCRFile"]
