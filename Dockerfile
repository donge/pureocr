#
# Build the pureocr CLI for one target platform. The ONNX Runtime library and
# the models are embedded, so the result is a single self-contained binary
# (about 45 MB) with no runtime dependencies besides glibc.
#
#   docker buildx build --platform linux/amd64 --output type=local,dest=bin/linux-amd64 .
#   docker buildx build --platform linux/arm64 --output type=local,dest=bin/linux-arm64 .
#
# or simply: make image

FROM --platform=$TARGETPLATFORM golang:1.24-bookworm AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/ocr ./example \
 && CGO_ENABLED=1 GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/bench ./bench

FROM scratch AS out
COPY --from=build /out/ocr /ocr
COPY --from=build /out/bench /bench
