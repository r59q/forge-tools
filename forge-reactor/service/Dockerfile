# syntax=docker/dockerfile:1
# Build from the repository root:
#   docker build -f forge-reactor/service/Dockerfile -t forge-reactor-service .

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY forge-core/forgeapi/go.mod forge-core/forgeapi/
COPY forge-core/servicekit/go.mod forge-core/servicekit/go.sum forge-core/servicekit/
COPY forge-connector/library/go.mod forge-connector/library/go.sum forge-connector/library/
COPY forge-reactor/library/go.mod forge-reactor/library/go.sum forge-reactor/library/
COPY forge-reactor/service/go.mod forge-reactor/service/go.sum forge-reactor/service/
WORKDIR /src/forge-reactor/service
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY forge-core/forgeapi/ forge-core/forgeapi/
COPY forge-core/servicekit/ forge-core/servicekit/
COPY forge-connector/library/ forge-connector/library/
COPY forge-reactor/library/ forge-reactor/library/
COPY forge-reactor/service/ forge-reactor/service/
WORKDIR /src/forge-reactor/service
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/forge-reactor-service ./cmd/forge-reactor-service

RUN mkdir -p /out/data

FROM alpine:3.23@sha256:ff7f0c727dd796aa36787bfcd9a64bf35a6ce72a73451b3359fde849b6703d63
RUN apk add --no-cache ca-certificates && \
    addgroup -g 65532 reactor && adduser -D -u 65532 -G reactor reactor
COPY --from=build /out/forge-reactor-service /usr/local/bin/forge-reactor-service
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME ["/data"]
ENV FORGE_REACTOR_DATA_DIR=/data \
    FORGE_REACTOR_GRPC_LISTEN=0.0.0.0:9090 \
    FORGE_REACTOR_HTTP_LISTEN=0.0.0.0:8080
EXPOSE 8080 9090
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/forge-reactor-service"]
