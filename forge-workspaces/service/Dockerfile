# syntax=docker/dockerfile:1
# Build from the repository root:
#   docker build -f forge-workspaces/service/Dockerfile -t forge-workspaces-service .

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY forge-core/forgeapi/go.mod forge-core/forgeapi/
COPY forge-connector/library/go.mod forge-connector/library/go.sum forge-connector/library/
COPY forge-workspaces/library/go.mod forge-workspaces/library/
COPY forge-workspaces/service/go.mod forge-workspaces/service/go.sum forge-workspaces/service/
WORKDIR /src/forge-workspaces/service
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY forge-core/forgeapi/ forge-core/forgeapi/
COPY forge-connector/library/ forge-connector/library/
COPY forge-workspaces/library/ forge-workspaces/library/
COPY forge-workspaces/service/ forge-workspaces/service/
WORKDIR /src/forge-workspaces/service
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/forge-workspaces-service ./cmd/forge-workspaces-service

RUN mkdir -p /out/data

FROM alpine:3.23@sha256:ff7f0c727dd796aa36787bfcd9a64bf35a6ce72a73451b3359fde849b6703d63
RUN apk add --no-cache git ca-certificates && \
    addgroup -g 65532 workspace && adduser -D -u 65532 -G workspace workspace
COPY --from=build /out/forge-workspaces-service /usr/local/bin/forge-workspaces-service
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME ["/data"]
ENV FORGE_WORKSPACES_DATA_DIR=/data \
    FORGE_WORKSPACES_GRPC_LISTEN=0.0.0.0:9090 \
    FORGE_WORKSPACES_HTTP_LISTEN=0.0.0.0:8080
EXPOSE 8080 9090
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/forge-workspaces-service"]
