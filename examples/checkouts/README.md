# Checkout code explorer

A small Node.js example using only **forge-checkouts** and **forge-connector**.
No framework or npm packages. Create independent checkouts, browse their files,
search text, inspect changes, and retain or remove working copies.

## Try it

Requires Node.js 24+ and Docker Compose. From this directory:

```sh
node setup.mjs                 # create private .env secrets
docker compose up --build -d
node --env-file=.env setup.mjs register
```

Open **http://127.0.0.1:3030**. If registration reports the broker is still
starting, rerun the last command. The local demo works even before registration.

1. Create a checkout from **Local demo repository** at `v1`.
2. Open `src/greeting.js`, or search for `greeting`.
3. Create another checkout at `main`. Its greeting says “Welcome” instead of
   “Hello”. Switch between checkouts: both keep their own checked-out files.
4. Use **Status & diff**, **Retain**, and **Remove** to try the lifecycle.

To see changes and protected cleanup, copy a checkout's `/data/copies/<id>`
location from the page and edit a file on the checkout host:

```sh
docker compose exec checkouts sh -c \
  'echo "// A local experiment" >> /data/copies/<id>/src/greeting.js'
```

**Status & diff** now shows an unstaged change; **Remove** refuses to discard it.
The other checkout stays unchanged. The example UI never forces removal.

## Explore a connected repository

Enter a forge token with repository read access, then send it to the broker:

```sh
read -rs FORGE_TOKEN; export FORGE_TOKEN
node --env-file=.env setup.mjs connect github
unset FORGE_TOKEN
```

For GitLab use `connect gitlab`. For a self-hosted Forgejo, supply its own token
and API URL (replace `forge.example` with your host):

```sh
FORGE_API_URL=https://forge.example/api/v1 \
FORGE_TOKEN="$FORGEJO_TOKEN" \
node --env-file=.env setup.mjs connect forgejo
```

The `connect` argument selects the provider. Forgejo requires its instance's
`/api/v1` URL; it has no default host. A Forgejo token cannot authenticate to GitHub.
`FORGE_GIT_URL` optionally overrides the Git base URL. Reload the page, select
that connection, enter `owner/repository` (or a GitLab group path), and choose a
branch, tag or commit. The explorer never receives the forge token.

This example registers two clients on its own broker: the proxy can issue
credentials; the checkout/explorer client can use all its connections but
cannot issue credentials. No management or other forge-tool service is needed.

## How it works

- `setup.mjs` creates secrets and configures the connector broker.
- `seed.sh` creates the two-revision local demo, then starts forge-checkouts.
- `server.mjs` calls checkout REST endpoints and the connector's connection list.
- `files.mjs` reads and searches checked-out files with Node's filesystem API.
- `public/` is plain HTML, CSS and browser JavaScript.

The explorer and checkout service mount the same `/data` volume; the explorer's
mount is read-only. Checkout paths are host-local, so a remote API URL alone is
not enough to read their files. This illustrates how another program can use a
checkout without implementing Git checkout or credential handling itself.

The example server exposes these browser routes (all JSON except static assets):

| Route                                       | Purpose                                                                             |
| ------------------------------------------- | ----------------------------------------------------------------------------------- |
| `GET /api/connections`                      | Connector connection choices.                                                       |
| `GET /api/checkouts`                        | Saved checkout metadata.                                                            |
| `POST /api/checkouts`                       | Create with `{source, repository, reference}`; empty source selects the local demo. |
| `GET /api/checkouts/{id}`                   | One checkout's metadata.                                                            |
| `DELETE /api/checkouts/{id}`                | Protected removal; never forwards `discard`.                                        |
| `POST /api/checkouts/{id}/retain`           | Retain the checkout.                                                                |
| `GET /api/checkouts/{id}/status` or `/diff` | Forward checkout status or base64 patches.                                          |
| `GET /api/checkouts/{id}/files`             | List local checkout files.                                                          |
| `GET /api/checkouts/{id}/file?path=`        | Read one text file.                                                                 |
| `GET /api/checkouts/{id}/search?q=`         | Search checkout text.                                                               |

File reads skip symlinks, `.git`, binary files and files over 256 KiB. Listings
stop after 2,000 entries; searches stop after 100 matches or 16 MiB of text. The
UI reports these limits. Repository content is displayed as text, never executed.
This is a local, single-user example with loopback ports, not a multi-user app
or a sandbox for concurrently hostile filesystem changes.

## Upgrade an existing example

If you ran `examples/workspaces`, stop that stack before updating and keep its
volumes. Copy its ignored `.env` into this directory if needed, then rename its
`FORGE_WORKSPACES_` keys to `FORGE_CHECKOUTS_`, preserving their values. To reuse
its data with the original default project name, add these lines to `.env`:

```dotenv
COMPOSE_PROJECT_NAME=workspaces
FORGE_CHECKOUTS_DATA_VOLUME=workspaces_workspace-data
```

Use your actual previous project/volume names if they differ. Rerun the start
and register commands above. Existing files stay at `/data/copies/<id>`; the
[service migrates their metadata](../../forge-checkouts/service/README.md#upgrade-from-forge-workspaces).

## Test and stop

```sh
npm test                      # no npm install needed
docker compose down           # keep checkouts and broker data
```

Checkouts survive restarts, including retained copies. `docker compose down -v`
deletes all example data, including uncommitted work; remove checkouts through
the UI first to apply cleanup protections. `.env` is kept and ignored by Git.

[MIT](../../LICENSE)
