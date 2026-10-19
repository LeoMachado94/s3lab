# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/s3lab ./cmd/s3lab
RUN mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="s3lab" \
      org.opencontainers.image.description="S3-compatible server for development and testing" \
      org.opencontainers.image.source="https://github.com/LeoMachado94/s3lab" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"

COPY --from=builder /out/s3lab /s3lab
COPY --from=builder --chown=65532:65532 /out/data /data

ENV S3LAB_DATA_DIR=/data
VOLUME /data
EXPOSE 9000 9001
USER 65532:65532
HEALTHCHECK --interval=5s --timeout=3s --start-period=2s --retries=5 CMD ["/s3lab", "healthcheck"]
ENTRYPOINT ["/s3lab"]
