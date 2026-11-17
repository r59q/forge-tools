# syntax=docker/dockerfile:1
# Build from the repository root:
#   docker build -f forge-agents/service/Dockerfile -t forge-agents-service .

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY forge-core/forgeapi/go.mod forge-core/forgeapi/
COPY forge-core/servicekit/go.mod forge-core/servicekit/go.sum forge-core/servicekit/
COPY forge-connector/library/go.mod forge-connector/library/go.sum forge-connector/library/
COPY forge-checkouts/library/go.mod forge-checkouts/library/
COPY forge-checkouts/service/go.mod forge-checkouts/service/go.sum forge-checkouts/service/
COPY forge-agents/library/go.mod forge-agents/library/
COPY forge-agents/service/go.mod forge-agents/service/go.sum forge-agents/service/
WORKDIR /src/forge-agents/service
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY forge-core/forgeapi/ forge-core/forgeapi/
COPY forge-core/servicekit/ forge-core/servicekit/
COPY forge-connector/library/ forge-connector/library/
COPY forge-checkouts/library/ forge-checkouts/library/
COPY forge-checkouts/service/ forge-checkouts/service/
COPY forge-agents/library/ forge-agents/library/
COPY forge-agents/service/ forge-agents/service/
WORKDIR /src/forge-agents/service
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/forge-agents-service ./cmd/forge-agents-service

# The service refuses a data directory others can read.
RUN mkdir -m 0700 -p /out/agents-data

# The three agent CLIs are Node packages, pinned so an image rebuild never
# changes the event formats the adapters parse.
FROM node:24-alpine@sha256:8a32c8cf649a49915a66e81e7a609223c708d0adabf288ee3f4077d765542f85
RUN apk add --no-cache git ca-certificates ripgrep libgcc libstdc++ && \
    npm install -g --no-fund --no-audit \
      @anthropic-ai/claude-code@2.1.326 \
      @openai/codex@0.169.0 \
      @google/gemini-cli@0.68.0 && \
    npm cache clean --force && \
    addgroup -g 65532 agent && adduser -D -u 65532 -G agent -h /home/agent agent
COPY --from=build /out/forge-agents-service /usr/local/bin/forge-agents-service
COPY --from=build --chown=65532:65532 /out/agents-data /agents-data
VOLUME ["/agents-data"]
# The CLIs keep their configuration and sessions under HOME, so it must be
# writable by the service user.
ENV HOME=/home/agent \
    FORGE_AGENTS_DATA_DIR=/agents-data \
    FORGE_AGENTS_GRPC_LISTEN=0.0.0.0:9090 \
    FORGE_AGENTS_HTTP_LISTEN=0.0.0.0:8080
EXPOSE 8080 9090
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/forge-agents-service"]
