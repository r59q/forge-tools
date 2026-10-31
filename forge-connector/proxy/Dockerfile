# syntax=docker/dockerfile:1
# Build from the repository root:
#   docker build -f forge-connector/proxy/Dockerfile -t forge-connector-proxy .

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY forge-core/forgeapi/go.mod forge-core/forgeapi/
COPY forge-connector/library/go.mod forge-connector/library/go.sum forge-connector/library/
COPY forge-connector/proxy/go.mod forge-connector/proxy/go.sum forge-connector/proxy/
WORKDIR /src/forge-connector/proxy
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY forge-core/forgeapi/ forge-core/forgeapi/
COPY forge-connector/library/ forge-connector/library/
COPY forge-connector/proxy/ forge-connector/proxy/
WORKDIR /src/forge-connector/proxy
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/forge-connector-proxy ./cmd/forge-connector-proxy

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/forge-connector-proxy /usr/local/bin/forge-connector-proxy
ENV FORGE_PROXY_HTTP_LISTEN=0.0.0.0:8081 \
    FORGE_PROXY_GRPC_LISTEN=0.0.0.0:9091
EXPOSE 8081 9091
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/forge-connector-proxy"]
