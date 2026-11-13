# syntax=docker/dockerfile:1
# Build from the repository root:
#   docker build -f forge-symbols/service/Dockerfile -t forge-symbols-service .

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY forge-core/forgeapi/go.mod forge-core/forgeapi/
COPY forge-core/servicekit/go.mod forge-core/servicekit/go.sum forge-core/servicekit/
COPY forge-symbols/library/go.mod forge-symbols/library/go.sum forge-symbols/library/
COPY forge-symbols/service/go.mod forge-symbols/service/go.sum forge-symbols/service/
WORKDIR /src/forge-symbols/service
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY forge-core/forgeapi/ forge-core/forgeapi/
COPY forge-core/servicekit/ forge-core/servicekit/
COPY forge-symbols/library/ forge-symbols/library/
COPY forge-symbols/service/ forge-symbols/service/
WORKDIR /src/forge-symbols/service
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/forge-symbols-service ./cmd/forge-symbols-service

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/forge-symbols-service /usr/local/bin/forge-symbols-service
ENV FORGE_SYMBOLS_GRPC_LISTEN=0.0.0.0:9090 \
    FORGE_SYMBOLS_HTTP_LISTEN=0.0.0.0:8080
EXPOSE 8080 9090
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/forge-symbols-service"]
