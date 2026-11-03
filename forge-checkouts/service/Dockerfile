# syntax=docker/dockerfile:1
# Build from the repository root:
#   docker build -f forge-checkouts/service/Dockerfile -t forge-checkouts-service .

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY forge-core/forgeapi/go.mod forge-core/forgeapi/
COPY forge-core/servicekit/go.mod forge-core/servicekit/go.sum forge-core/servicekit/
COPY forge-connector/library/go.mod forge-connector/library/go.sum forge-connector/library/
COPY forge-checkouts/library/go.mod forge-checkouts/library/
COPY forge-checkouts/service/go.mod forge-checkouts/service/go.sum forge-checkouts/service/
WORKDIR /src/forge-checkouts/service
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY forge-core/forgeapi/ forge-core/forgeapi/
COPY forge-core/servicekit/ forge-core/servicekit/
COPY forge-connector/library/ forge-connector/library/
COPY forge-checkouts/library/ forge-checkouts/library/
COPY forge-checkouts/service/ forge-checkouts/service/
WORKDIR /src/forge-checkouts/service
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/forge-checkouts-service ./cmd/forge-checkouts-service

RUN mkdir -p /out/data

FROM alpine:3.23@sha256:ff7f0c727dd796aa36787bfcd9a64bf35a6ce72a73451b3359fde849b6703d63
RUN apk add --no-cache git ca-certificates && \
    addgroup -g 65532 checkout && adduser -D -u 65532 -G checkout checkout
COPY --from=build /out/forge-checkouts-service /usr/local/bin/forge-checkouts-service
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME ["/data"]
ENV FORGE_CHECKOUTS_DATA_DIR=/data \
    FORGE_CHECKOUTS_GRPC_LISTEN=0.0.0.0:9090 \
    FORGE_CHECKOUTS_HTTP_LISTEN=0.0.0.0:8080
EXPOSE 8080 9090
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/forge-checkouts-service"]
