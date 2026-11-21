# forge-chat frontend

Framework-agnostic TypeScript chat for forge-agents. Requires Node 24.

```sh
npm ci
npm run lint
npm test
npm pack
```

For browser tests, run `npx playwright install chromium` and
`npm run test:browser` after building. Set `CHROMIUM_EXECUTABLE` to use
an installed Chromium instead.

`npm run build` also produces the standalone app in `dist/web`. Serve that
directory with [forge-chat service](../service/), which supplies the API
routes. The standalone app holds the chat service token in memory while signed in.

To embed the built package, import its stylesheet and mount into an existing
element. The host can supply its own provider, or use the REST provider
against a same-origin chat service.

```ts
import { httpChat, mountChat } from "@r59q/chat";
import "@r59q/chat/styles.css";

const unmount = mountChat(container, httpChat({ token: () => chatServiceToken }));
// Call unmount when the container is removed.
```

To start a chat from a host's repository selection, pass the selected
`{ sourceId, path }` pairs in the mount options:

```ts
const unmount = mountChat(container, provider, { repositories });
```

The operator chooses a connection and path together. New chats use a forge
checkout for that pair. Provide a nonempty selection to enable creation.
The host supplies `provider` with chat access. The REST provider uses the
[chat service token](../service/), which grants deployment-wide chat access.
