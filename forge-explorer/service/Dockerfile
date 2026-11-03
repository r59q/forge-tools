# syntax=docker/dockerfile:1
# Build from repository root. Node produces static files; Go never embeds/builds them.
FROM --platform=$BUILDPLATFORM node:24-alpine@sha256:8a32c8cf649a49915a66e81e7a609223c708d0adabf288ee3f4077d765542f85 AS frontend
WORKDIR /frontend
COPY forge-explorer/frontend/package.json forge-explorer/frontend/package-lock.json ./
RUN npm ci --ignore-scripts
COPY forge-explorer/frontend/ ./
RUN npm run typecheck && npm run build

FROM --platform=$BUILDPLATFORM golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS backend
WORKDIR /src
COPY forge-core/forgeapi/ forge-core/forgeapi/
COPY forge-core/servicekit/ forge-core/servicekit/
COPY forge-explorer/library/ forge-explorer/library/
COPY forge-explorer/service/ forge-explorer/service/
WORKDIR /src/forge-explorer/service
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/forge-explorer-service ./cmd/forge-explorer-service

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=backend /out/forge-explorer-service /usr/local/bin/forge-explorer-service
COPY --from=frontend /frontend/dist/app /web
ENV FORGE_EXPLORER_ASSETS=/web \
    FORGE_EXPLORER_GRPC_LISTEN=0.0.0.0:9090 \
    FORGE_EXPLORER_HTTP_LISTEN=0.0.0.0:8080
EXPOSE 8080 9090
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/forge-explorer-service"]
