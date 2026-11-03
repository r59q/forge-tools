# Embedded explorer

A small host app that installs [`@r59q/explorer`](../../forge-explorer/frontend/)
as an npm dependency and embeds it in its own page. No bundler or framework.

## Try it

Requires Node.js 24+ and Docker Compose. From this directory:

1. Install the package. `.npmrc` points the `@r59q` scope at GitHub Packages,
   which needs a token (classic) with `read:packages`:

   ```sh
   npm login --scope=@r59q --auth-type=legacy --registry=https://npm.pkg.github.com
   npm install
   ```

   Or build it from this repository, without registry access:

   ```sh
   (cd ../../forge-explorer/frontend && npm ci && npm run build && npm pack)
   npm install --no-save ../../forge-explorer/frontend/r59q-explorer-*.tgz
   ```

2. Create a service token and start the explorer service. It serves
   [sample/](sample/) read-only:

   ```sh
   echo "FORGE_EXPLORER_TOKEN=$(openssl rand -hex 32)" > .env
   docker compose up --build -d
   ```

3. Start the host and open **http://127.0.0.1:3040**:

   ```sh
   npm start
   ```

Use the links above the explorer, or browse and search in it. The URL hash
follows the open file, so reloads and shared links reopen it.

## How it works

- [public/app.js](public/app.js) imports `createExplorer` and `httpProvider`
  from the package and mounts the explorer in `#files`. It opens the file named
  in the URL hash and shows the host's own status from `onLocation` and
  `onSelection`.
- [public/index.html](public/index.html) maps `@r59q/explorer` to
  `/vendor/explorer.js` with an import map. With a bundler, the same import
  works without the map.
- [server.mjs](server.mjs) serves the page, the package's module, and forwards
  `/explorer-api/v1/explorer/*` to the service. It adds the service token
  there, so the browser never sees it. A real host would check its own users
  first: every caller with the token can read every configured root.
- [public/style.css](public/style.css) sets `--explorer-accent` on the host
  element. The explorer's styles live in its shadow root.

Set `EXPLORER_URL` and `PORT` to change the service address (default
`http://127.0.0.1:8090`) or the host port.

## Test and stop

```sh
npm test               # needs the package installed
docker compose down
```

[MIT](../../LICENSE)
