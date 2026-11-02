# syntax=docker/dockerfile:1
# Build from the repository root:
#   docker build -f forge-changes/service/Dockerfile -t forge-changes-service .

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY forge-core/forgeapi/go.mod forge-core/forgeapi/
COPY forge-connector/library/go.mod forge-connector/library/go.sum forge-connector/library/
COPY forge-changes/library/go.mod forge-changes/library/
COPY forge-changes/service/go.mod forge-changes/service/go.sum forge-changes/service/
WORKDIR /src/forge-changes/service
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY forge-core/forgeapi/ forge-core/forgeapi/
COPY forge-connector/library/ forge-connector/library/
COPY forge-changes/library/ forge-changes/library/
COPY forge-changes/service/ forge-changes/service/
WORKDIR /src/forge-changes/service
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/forge-changes-service ./cmd/forge-changes-service

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/forge-changes-service /usr/local/bin/forge-changes-service
ENV FORGE_CHANGES_GRPC_LISTEN=0.0.0.0:9090 \
    FORGE_CHANGES_HTTP_LISTEN=0.0.0.0:8080
EXPOSE 8080 9090
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/forge-changes-service"]
