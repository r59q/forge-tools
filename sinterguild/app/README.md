# SinterGuild

Connect a forge, pick repositories, and start a chat in a worktree from a
single mobile-friendly page.

## Run with Docker Compose

Follow the [full-stack setup](../../examples/fullstack/README.md#start) and
open `http://127.0.0.1:8098/`. Sign in with the management operator token,
add a forge connection, and confirm the repositories. Enter the chat service
token to open chat. Choose **New chat**, select a repository, and send a
message. The chosen agent client needs its API key configured in the stack.

The app saves the repository identities in local storage for this origin.
Later visits go straight to chat sign-in. Chat authorization covers the
whole deployment. The selection only supplies choices for new chats.

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
test drives the built app through the same gateway used by Compose, with
deterministic management and chat services.

Run `npm start` after building. By default the app listens at
`http://127.0.0.1:8098/` and forwards to the management and chat ports
published by the full-stack example. Stop the Compose SinterGuild container
first to free that port, or choose another local port.

| Environment variable | Default |
| --- | --- |
| `SINTERGUILD_HOST` | `127.0.0.1` |
| `SINTERGUILD_PORT` | `8098` |
| `SINTERGUILD_MANAGEMENT_URL` | `http://127.0.0.1:8083` |
| `SINTERGUILD_CHAT_URL` | `http://127.0.0.1:8097` |

Build the container from the repository root with
`docker build -f sinterguild/app/Dockerfile -t sinterguild-app .`.

## Browser API

The same-origin gateway forwards the management connection and repository
requests under `/api/management/v1/` and the chat metadata and agent requests
under `/api/chat/v1/`. The browser submits the corresponding operator or
chat bearer token, kept in memory for that step. Both services enforce their
own authorization on every request, including chat streams. The gateway
has no broker or agent credentials. Serve the app over HTTPS beyond
loopback, preserving the public Host and browser origin headers.

See [management](../../forge-tools-management/service/README.md#api) and
[chat](../../forge-chat/service/README.md#api) for the upstream API contracts.
