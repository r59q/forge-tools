// A host app that embeds forge-explorer. It serves its own page, the
// @r59q/explorer module, and forwards explorer API calls with the service
// token, so the token never reaches the browser.
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const API = "/explorer-api";
const MAX_BODY = 1 << 20;

const TYPES = {
  html: "text/html; charset=utf-8",
  js: "text/javascript; charset=utf-8",
  css: "text/css; charset=utf-8",
};

/** The only files the host serves, by URL path. */
export function assets(
  explorerModule = fileURLToPath(import.meta.resolve("@r59q/explorer")),
) {
  const page = (name) => fileURLToPath(new URL(`public/${name}`, import.meta.url));

  return {
    "/": page("index.html"),
    "/app.js": page("app.js"),
    "/style.css": page("style.css"),
    "/vendor/explorer.js": explorerModule,
  };
}

/** Creates the request handler. explorerURL is the explorer service's REST address. */
export function createHost({ explorerURL, token, files = assets() }) {
  const upstream = explorerURL.replace(/\/$/, "");

  return async (request, response) => {
    const url = new URL(request.url, "http://host");

    try {
      if (url.pathname.startsWith(`${API}/v1/explorer/`)) {
        const target = upstream + url.pathname.slice(API.length) + url.search;
        await forward(request, response, target, token);
      } else {
        await serve(response, files[url.pathname]);
      }
    } catch (error) {
      const status = error instanceof TooLarge ? 413 : 502;
      send(response, status, "application/json", JSON.stringify({ code: 14 }));
    }
  };
}

async function serve(response, file) {
  if (!file) {
    send(response, 404, "text/plain", "Not found");
    return;
  }

  const type = TYPES[file.split(".").pop()] ?? "application/octet-stream";
  send(response, 200, type, await readFile(file));
}

// forward replaces whatever Authorization the browser sent with the service token.
async function forward(request, response, target, token) {
  if (request.method !== "GET" && request.method !== "POST") {
    send(response, 405, "text/plain", "Method not allowed");
    return;
  }

  const body = request.method === "POST" ? await readBody(request) : undefined;
  const headers = { Authorization: `Bearer ${token}` };
  if (body) headers["Content-Type"] = "application/json";

  const reply = await fetch(target, {
    method: request.method,
    headers,
    body,
    redirect: "error",
  });

  const type = reply.headers.get("content-type") ?? "application/json";
  send(response, reply.status, type, Buffer.from(await reply.arrayBuffer()));
}

class TooLarge extends Error {}

async function readBody(request) {
  const chunks = [];
  let size = 0;

  for await (const chunk of request) {
    size += chunk.length;
    if (size > MAX_BODY) throw new TooLarge();
    chunks.push(chunk);
  }

  return Buffer.concat(chunks);
}

function send(response, status, type, body) {
  response.writeHead(status, {
    "Content-Type": type,
    "Cache-Control": "no-store",
    "X-Content-Type-Options": "nosniff",
  });
  response.end(body);
}

if (import.meta.main) {
  const token = process.env.FORGE_EXPLORER_TOKEN;
  if (!token) throw new Error("Set FORGE_EXPLORER_TOKEN; see README.");

  const explorerURL = process.env.EXPLORER_URL ?? "http://127.0.0.1:8090";
  const port = Number(process.env.PORT ?? 3040);

  createServer(createHost({ explorerURL, token })).listen(port, "127.0.0.1", () => {
    console.log(`Open http://127.0.0.1:${port}`);
  });
}
