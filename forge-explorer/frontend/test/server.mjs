// A frontend-only HTTP contract fixture. The same browser suite also runs against the Go service.
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { resolve, extname } from "node:path";
import { createHash } from "node:crypto";

const root = resolve("dist/app");
const range = { startLine: 0, endLine: 0, startColumn: 0, endColumn: 0 };
const state = { skipped: 0, truncated: false, canceled: false };
const files = (side) => ({
  "hello.go": `package ${side}\n// needle\nvar text = "<img src=x onerror=alert(1)>"`,
  "sub/other.ts": "const value = 42;",
});
const sources = ["new", "old"].map((id) => ({
  id,
  label: id,
  version: `${id}-root`,
  kind: "worktree",
  capabilities: {
    symbols: false,
    definitions: false,
    references: false,
    history: false,
  },
  limits: {
    fileBytes: 1048576,
    entries: 10000,
    results: 200,
    searchBytes: 67108864,
    depth: 64,
  },
}));
function file(source, path) {
  const text = files(source)[path];
  if (text === undefined) return undefined;
  return {
    text,
    location: {
      source,
      path,
      version: createHash("sha256")
        .update(source + text)
        .digest("hex"),
      range,
    },
  };
}
function reply(response, status, body) {
  response.writeHead(status, { "Content-Type": "application/json" });
  response.end(JSON.stringify(body));
}
const server = createServer(async (request, response) => {
  const url = new URL(request.url, "http://localhost");
  if (!url.pathname.startsWith("/v1/")) {
    const name = url.pathname === "/" ? "index.html" : url.pathname.slice(1);
    const path = resolve(root, name);
    if (!path.startsWith(root + "/")) {
      response.writeHead(404).end();
      return;
    }
    try {
      const data = await readFile(path);
      response.writeHead(200, {
        "Content-Type":
          {
            ".html": "text/html",
            ".js": "text/javascript",
            ".css": "text/css",
          }[extname(path)] ?? "text/plain",
      });
      response.end(data);
    } catch {
      response.writeHead(404).end();
    }
    return;
  }
  if (request.headers.authorization !== "Bearer test-only") {
    reply(response, 401, { code: 16 });
    return;
  }
  if (url.pathname === "/v1/explorer/sources") {
    reply(response, 200, { sources });
    return;
  }
  const [, source, operation] =
    url.pathname.match(/\/sources\/([^/]+)\/(tree|file|search)$/) ?? [];
  if (!sources.some((value) => value.id === source)) {
    reply(response, 404, { code: 5 });
    return;
  }
  if (
    operation !== "search" &&
    url.searchParams.get("sourceVersion") !== `${source}-root`
  ) {
    reply(response, 400, { code: 9 });
    return;
  }
  const path = url.searchParams.get("path");
  if (operation === "file") {
    const content = file(source, path);
    if (!content) {
      reply(response, 404, { code: 5 });
      return;
    }
    const expected = url.searchParams.get("expectedVersion");
    if (expected && expected !== content.location.version) {
      reply(response, 400, { code: 9 });
      return;
    }
    reply(response, 200, { file: content });
  } else if (operation === "tree") {
    const entries =
      path === "."
        ? [
            { path: "hello.go", kind: "file" },
            { path: "sub", kind: "directory" },
          ]
        : [{ path: "sub/other.ts", kind: "file" }];
    reply(response, 200, {
      directory: { source, version: "listing", entries, state },
    });
  } else {
    let body = "";
    for await (const chunk of request) body += chunk;
    const query = JSON.parse(body);
    if (query.sourceVersion !== `${source}-root`) {
      reply(response, 400, { code: 9 });
      return;
    }
    const matches = [];
    for (const path of Object.keys(files(source))) {
      const content = file(source, path);
      if (
        query.files
          ? path.includes(query.text)
          : content.text.includes(query.text)
      ) {
        matches.push({
          location: {
            ...content.location,
            range: query.files
              ? range
              : { startLine: 2, endLine: 2, startColumn: 4, endColumn: 10 },
          },
          preview: query.files ? path : "// needle",
        });
      }
    }
    reply(response, 200, { results: { matches, state } });
  }
});
server.listen(18189, "127.0.0.1");
