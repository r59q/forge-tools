# SinterGuild connection setup

Mount the first-run forge connection form in a SinterGuild host. Include
`@r59q/sinterguild-connection/styles.css` in the host and set
`<meta name="viewport" content="width=device-width, initial-scale=1">`.

```ts
import { mountConnectionSetup } from "@r59q/sinterguild-connection";

mountConnectionSetup(container, {
  managementFetch,
  onConnected: ({ id }) => openRepositoryPicker(id),
});
```

The host provides a fetch-compatible transport for `POST /v1/connections`
and applies operator authorization. Keep management same-origin, for example
through the host's authenticated proxy. The host handles authentication and
navigation. The callback receives only the new public connection ID, which
matches the source ID in management repository listings.

The access token travels in the request body. The form clears it on submit,
keeps the other fields for retry, and uses fixed errors. Forge-connector
validates and stores the credential.

## Build and test

From this folder with Node.js 24 or newer:

```sh
npm ci --ignore-scripts
npm run lint
npm test
npx playwright install chromium
npm run test:browser
```

Set `CHROMIUM_EXECUTABLE` to use an installed Chromium. The headless browser
test mounts the form in a fixture host at 320px and exercises rejection,
retry and handoff. The management service tests cover the REST endpoint
against a broker and fake forge.
