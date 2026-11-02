FROM node:24-alpine@sha256:8a32c8cf649a49915a66e81e7a609223c708d0adabf288ee3f4077d765542f85
WORKDIR /app
COPY package.json server.mjs files.mjs ./
COPY public/ public/
RUN mkdir /data && chown 65532:65532 /data
USER 65532:65532
EXPOSE 3030
CMD ["node", "server.mjs"]
