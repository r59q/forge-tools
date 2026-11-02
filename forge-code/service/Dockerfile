# syntax=docker/dockerfile:1
# Build from the repository root:
#   docker build -f forge-code/service/Dockerfile -t forge-code-service .

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY forge-core/forgeapi/go.mod forge-core/forgeapi/
COPY forge-connector/library/go.mod forge-connector/library/go.sum forge-connector/library/
COPY forge-code/library/go.mod forge-code/library/
COPY forge-code/service/go.mod forge-code/service/go.sum forge-code/service/
WORKDIR /src/forge-code/service
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY forge-core/forgeapi/ forge-core/forgeapi/
COPY forge-connector/library/ forge-connector/library/
COPY forge-code/library/ forge-code/library/
COPY forge-code/service/ forge-code/service/
WORKDIR /src/forge-code/service
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/forge-code-service ./cmd/forge-code-service

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/forge-code-service /usr/local/bin/forge-code-service
ENV FORGE_CODE_GRPC_LISTEN=0.0.0.0:9090 \
    FORGE_CODE_HTTP_LISTEN=0.0.0.0:8080
EXPOSE 8080 9090
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/forge-code-service"]
