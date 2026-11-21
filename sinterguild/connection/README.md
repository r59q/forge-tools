# SinterGuild connection setup and repository picker

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

## Repository picker

Mount the picker after connecting a forge or when reopening the selection.
Use the same stylesheet, viewport and authorized transport as connection setup.

```ts
import { mountRepositoryPicker } from "@r59q/sinterguild-connection/repository-picker";

await mountRepositoryPicker(container, {
  managementFetch,
  deploymentId: "production",
  onComplete: repositories => openAgentInterface(repositories),
});
```

The picker loads `GET /v1/repositories` across all connected sources.
Confirmation saves the selected `{ sourceId, path }` pairs and passes them
to the host, which opens the agent interface. An unsuccessful or empty listing
can be reloaded. Hosts can supply `onAddConnection` to open connection setup
and `onBack` in the setup form to return to the picker.

Use a stable, public deployment ID to scope selection within the host origin.
Reopening the same deployment in the same browser profile checks the saved
repositories that are still visible. Confirmation replaces the saved set and
requires writable local storage.

Use `loadRepositorySelection(deploymentId)` to restore a completed preference
before mounting the picker. An empty result means setup is needed.

Pass `repositories` directly to [forge-chat](../../forge-chat/frontend/)
with `mountChat(container, chatProvider, { repositories })`. Load the chat
stylesheet in the host and supply a provider with chat service access.

## Build and test

From this folder with Node.js 24 or newer:

```sh
npm ci --ignore-scripts
npm run lint
npm test
npx playwright install chromium
npm run test:browser
```

Set `CHROMIUM_EXECUTABLE` to use an installed Chromium. The headless fixture
tests exercise connection retry, repository listing states, touch selection at
320 px and restoration after reopening the browser profile.
