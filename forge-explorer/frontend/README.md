# Explorer frontend

Framework-agnostic TypeScript components, styles and HTTP provider. No runtime
package dependencies; NudaUI source is committed and adapted locally.

From this directory, with Node 24+:

```sh
npm ci
npm run typecheck
npm run lint
npm test
npm run build
npm run test:package
npx playwright install chromium
npm run test:browser
```

Browser tests serve the production standalone app and two embedded instances
against a local HTTP contract fixture; they do not build a backend. To exercise
the same tests against the actual Go service, run `node forge-explorer/test-browser.mjs`
from repository root (Go, curl, jq and frontend dependencies must be installed).
`EXPLORER_CHROMIUM=/path/to/chromium` selects an existing browser executable.

`src/model/` holds state with no DOM: navigation, history, tabs and location
checks, plus search requests and cancellation. `src/components/` holds views that
render it. See the [component catalogue](COMPONENTS.md) for named parts and states.
`npm run lint` applies the repository's shared [ESLint rules](../../eslint.rules.mjs).

The build emits `dist/explorer.js`, public declarations in `dist/types`,
`dist/styles.css`, and the standalone app in `dist/app`. `npm pack` produces the
consumable package, without the standalone app. The service serves `dist/app` as static files; Go checks need
no frontend build. Normal builds, tests and runtime never contact NudaUI.

The [frontend workflow](../../.github/workflows/forge-explorer-frontend.yml)
runs type checks, unit tests, production builds and browser tests on pull requests
and changes to `master`. After checks pass on `master`, it builds and publishes
`@r59q/explorer` to `https://npm.pkg.github.com` using the repository's
`GITHUB_TOKEN`. No extra publishing secret is needed. Pull requests and forks
cannot run the publishing job. Manual dispatch on `master` can retry a release.

Existing versions are skipped; registry/authentication failures fail the job.
For a new release, bump the version and lockfile with
`npm version patch --no-git-tag-version`, then commit both files. Publishing is
serialized and never replaces a version. Package metadata links it to this
repository. GitHub initially creates packages as private; after the first
publication, a maintainer should set the package visibility to public if desired.
See [GitHub's registry documentation](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-npm-registry).

In a consuming project, configure the scope, authenticate with a personal access
token (classic) with `read:packages` and access to the package, then install:

```sh
npm config set @r59q:registry https://npm.pkg.github.com --location=project
npm login --scope=@r59q --auth-type=legacy --registry=https://npm.pkg.github.com
npm install @r59q/explorer
```

GitHub requires authentication even for public npm packages. Keep authentication
in your user npm configuration, never in committed files. To consume a local
build without registry access, run `npm pack` after building and install the
resulting `r59q-explorer-0.1.0.tgz` in the host project. The service image includes
the standalone app and has its own [publishing pipeline](../service/README.md#asset-packaging).

## Development

[compose.yaml](compose.yaml) runs the explorer service against real forge
repositories. The [connector](../../forge-connector/) holds your forge token,
[checkouts](../../forge-checkouts/service/) clones the repositories through
it, and the explorer service reads those checkouts. It needs Docker Compose,
`curl`, `jq` and `openssl`.

```sh
npm run dev:setup     # first run creates .env and stops
$EDITOR .env          # set FORGE_DEV_TOKEN and FORGE_DEV_REPOSITORIES
npm run dev:setup     # starts the stack, connects the forge, checks out
npm run dev           # live reload on http://127.0.0.1:8091
```

`npm run dev` rebuilds and reloads the page after every change. It forwards
API calls to the explorer service with the service token, then connects right
away. It reads `FORGE_EXPLORER_TOKEN` and `EXPLORER_URL` (default
`http://127.0.0.1:8090`) from the environment or `.env`, so it also works
with a service started another way. `EXPLORER_DEV_PORT` changes its port.

| Variable                 | Meaning                                                 |
| ------------------------ | ------------------------------------------------------- |
| `FORGE_DEV_FORGE`        | `github`, `gitlab` or `forgejo`                         |
| `FORGE_DEV_HOST`         | Self-hosted forge host; empty for the public forges     |
| `FORGE_DEV_TOKEN`        | Forge token with read access to the repositories        |
| `FORGE_DEV_REPOSITORIES` | `owner/name` list, separated by spaces                  |
| `FORGE_DEV_REFERENCE`    | Branch, tag or commit to check out, default `HEAD`      |
| `FORGE_EXPLORER_ROOTS`   | Written by setup: one explorer source per checkout      |

Run `npm run dev:setup` again after changing them. It reuses the
`explorer-dev` connection, replaces its token and adds missing checkouts.
Each repository becomes a source named like `owner-name`. Until then, the
explorer shows this project's `src/`.

| Service    | Address                 | Sign in                                         |
| ---------- | ----------------------- | ----------------------------------------------- |
| Explorer   | `http://127.0.0.1:8190` | `FORGE_EXPLORER_TOKEN`                          |
| Management | `http://127.0.0.1:8183` | `operator` / `FORGE_MANAGEMENT_TOKEN`           |
| Checkouts  | `http://127.0.0.1:8188` | `Authorization: Bearer <FORGE_CHECKOUTS_TOKEN>` |

Ports differ from the [full-stack example](../../examples/fullstack/), so both
can run. After changing a service, run `docker compose up --build -d <name>`.
Checkouts do not follow the forge: remove one through the checkouts API to
get newer commits, or reset everything with `docker compose down -v`, which
deletes the stored forge token and all checkouts.

## Embedding contract

[examples/explorer-embed](../../examples/explorer-embed/) is a minimal host
that installs the package and embeds one explorer.

```ts
import { createExplorer, httpProvider } from '@r59q/explorer';

const lifetime = new AbortController();
const provider = httpProvider('/explorer-api', () => serviceToken);
const [source] = await provider.sources(lifetime.signal);
const explorer = createExplorer(document.querySelector('#files')!, provider, source, {
  onNavigate(location) { /* return false to intercept; host may route elsewhere */ },
  onLocation(location) { /* successful navigation */ },
  onSelection(location) { /* selected line range */ },
});
await explorer.open('src/main.go');
await explorer.back();
await explorer.forward();
await explorer.refresh(); // explicitly reads latest bytes, discarding stale selection
const location = explorer.current();
explorer.dispose(); // cancels calls, removes scoped listeners/DOM; host may be reused
lifetime.abort();
```

`ContentProvider` defines `sources`, `browse`, `read`, `search`, plus optional
`symbols`, `definitions`, `references`, `history`. All methods accept AbortSignal;
custom providers must honor it, verify source incarnation and expected file
versions, and return bounded data. The host owns shared providers. Disposal
cancels only the instance's work, not another instance's provider.

`Source` identifies an explicit content source and its incarnation. `Location`
contains source, file content version, slash path and a one-based line/UTF-16
column range (exclusive end column). Opening a string requests current content;
opening a Location checks its version. A failed/stale read leaves tabs/history
unchanged. Reload explicitly opens current bytes. `current()` and callbacks return
copies. Selection is whole-line (click, Shift-click or Shift-arrow); text remains
selectable for copying. Arrow/Home/End keys navigate lines; Alt-left/right navigate
history. Tabs use roving keyboard focus with Enter/Space activation and Delete to
close. Tree disclosures use native controls, with Up/Down focus navigation.

Semantics/history are capability-based provider extensions; the initial UI reports
them as unsupported for worktrees. Implementations must keep navigation within the
originating source/version. History metadata does not implicitly switch a source.
The HTTP provider implements only the initial backend capabilities.

Two hosts can mount independent instances with independent tabs, searches, history,
loading/error state and styles. Shadow DOM isolates component CSS and all listeners.
Override `--explorer-bg`, `--explorer-panel`, `--explorer-text`, `--explorer-muted`,
`--explorer-accent` on each host. The exported `styles` string and `styles.css` are
also available for custom composition; the standard component injects them itself.

[embedding.ts](examples/embedding.ts) demonstrates old/new sources and host-owned
counterpart routing. Normal navigation stays in its source; combined ambiguous
navigation defaults to new **in the host**, never in the explorer. Crossing sides
reads the counterpart afresh instead of reusing another side's version/range.
Run the service with roots named `old` and `new`, then open `/embedding.html`.

Rendering uses text nodes, including highlighting. Go, JS/TS, Python, JSON and
shell have a lightweight lexical highlighter; other extensions render plain text.
This is not semantic analysis. At most 200 code lines render at once; previous/next
pages and Go to line reach the entire bounded file. Search and tree limits and
skipped content are visible. See [source policies](../README.md).

## NudaUI source workflow

[Official developer docs](https://nudaui.dev/developers) describe the API.
Selected components: `animated-tabs` and `shine-sweep`. Their actual source was
retrieved from NudaUI's official source registry at the commit recorded in
[nudaui/manifest.json](nudaui/manifest.json). The live JSON endpoint returned HTTP
402 during initial import, so the command used its pinned registry fallback.
It parses literal `code` blocks with the TypeScript AST, without evaluating JSX,
expressions or scripts. The synthesized payload records this source honestly;
it is not represented as a response from the unavailable JSON endpoint.

- `nudaui/baseline/<id>/`: unchanged imported code and normalized payload.
- `nudaui/manifest.json`: IDs, API/source URLs, upstream revision where available,
  retrieval time, ETag, raw source checksum, payload and individual code checksums.
- `src/components/nuda/<id>/`: maintained working copies and deviation notes.
- `src/components/tabs.ts`, `controls.ts`: typed instance-scoped adapters.
- `nudaui/LICENSE`: full upstream MIT license and copyright notice, also shipped
  in bundles and the standalone app. Baseline source is never edited by hand.

Explicitly stage one component at a time:

```sh
npm run nuda -- stage animated-tabs
# Official registry fallback, pinned to an inspected upstream commit:
npm run nuda -- stage animated-tabs --registry accordions-tabs.tsx \
  --revision b7b2842a666b9e9709bdb68628ea8bd2eb0dc686
```

Inspect the printed baseline/local/incoming checksums and conflicts, then compare
`nudaui/.staging/<id>/payload.json`, `nudaui/baseline/<id>/` and the working files.
Staged `code.html`, `code.css` and optional `code.js` are also available for
`git diff --no-index nudaui/baseline/ID/code.css nudaui/.staging/ID/code.css`.
Nothing is applied during staging or normal builds. To apply inspected changes:

```sh
npm run nuda -- apply animated-tabs
```

Changed local working files block replacement. Reimporting identical source is a
no-op, preserving metadata and local edits. For a manual merge, first review the
three versions, then `apply animated-tabs --keep-working` to advance only the
baseline and preserve working code. Merge the incoming changes into working files
and typed adapters yourself, update deviation notes and run tests. Future updates
still flag working code that differs from the saved upstream baseline. Removed
upstream blocks require manual reconciliation; the importer never deletes them.
Concurrent local edits after staging require staging again. Keep review/merge
changes in Git; the tool is deliberately conservative rather than an automatic
three-way text merger. Direct base-component rewrites need no extra approval.

`npm test` exercises local fixtures, malformed payloads/IDs, literal registry
extraction, deterministic repeats, modified components, staged tampering, symlink
outputs and update conflicts. For manual fixtures use `stage ID --fixture FILE`;
fixture provenance is explicitly marked local. Importing never executes snippets.
