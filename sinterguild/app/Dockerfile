# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:24-alpine@sha256:8a32c8cf649a49915a66e81e7a609223c708d0adabf288ee3f4077d765542f85 AS build
WORKDIR /src/sinterguild/app
COPY sinterguild/app/package.json sinterguild/app/package-lock.json ./
RUN npm ci --ignore-scripts
COPY sinterguild/app/ ./
COPY sinterguild/connection/package.json /src/sinterguild/connection/
COPY sinterguild/connection/src/ /src/sinterguild/connection/src/
COPY forge-chat/frontend/package.json /src/forge-chat/frontend/
COPY forge-chat/frontend/src/ /src/forge-chat/frontend/src/
RUN npm run build

FROM node:24-alpine@sha256:8a32c8cf649a49915a66e81e7a609223c708d0adabf288ee3f4077d765542f85
WORKDIR /app
COPY sinterguild/app/server.mjs ./
COPY --from=build /src/sinterguild/app/dist/web/ ./dist/web/
ENV SINTERGUILD_HOST=0.0.0.0 SINTERGUILD_PORT=8080
USER node
EXPOSE 8080
CMD ["node", "server.mjs"]
