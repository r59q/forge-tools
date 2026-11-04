// Request handling for `npm run dev`. It serves the dev build, forwards
// explorer API calls with the service token and pushes reload events.
import { readFile } from "node:fs/promises";
import { extname, resolve, sep } from "node:path";

const API = "/v1/explorer/";
const RELOAD = "/dev/reload";
const CLIENT = "/dev/client.js";
const MAX_BODY = 1 << 20;

const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".txt": "text/plain; charset=utf-8",
};

/** devPage adds the dev client script to the app's index.html. */
export function devPage(html) {
  const tag = `<script type="module" src="${CLIENT}"></script>`;

  return html.replace("</head>", `  ${tag}\n  </head>`);
}

/**
 * createDevServer returns a request handler and a reload function.
 * root holds the built files, upstream is the explorer service's REST
 * address and token is its service token.
 */
export function createDevServer({ root, upstream, token, client }) {
  const base = upstream.replace(/\/$/, "");
  const pages = new Set();

  async function handle(request, response) {
    const { pathname, search } = new URL(request.url, "http://dev");

    try {
      if (pathname.startsWith(API)) {
        await forward(request, response, base + pathname + search, token);
      } else if (pathname === RELOAD) {
        subscribe(request, response, pages);
      } else if (pathname === CLIENT) {
        await sendFile(response, client);
      } else {
        await sendFile(response, staticFile(root, pathname));
      }
    } catch (error) {
      sendError(response, error);
    }
  }

  function reload() {
    for (const page of pages) {
      page.write("event: reload\ndata: {}\n\n");
    }
  }

  return { handle, reload };
}

// staticFile maps a URL path into root, or returns undefined outside it.
function staticFile(root, pathname) {
  const top = resolve(root);
  const name = pathname === "/" ? "index.html" : decodeURIComponent(pathname);
  const file = resolve(top, `.${sep}${name}`);
  const inside = file.startsWith(top + sep);

  return inside ? file : undefined;
}

function subscribe(request, response, pages) {
  response.writeHead(200, {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-store",
    Connection: "keep-alive",
  });
  response.write(": connected\n\n");
  pages.add(response);
  request.on("close", () => pages.delete(response));
}

async function sendFile(response, file) {
  const body = file ? await readFile(file).catch(() => undefined) : undefined;
  if (!body) {
    send(response, 404, "text/plain; charset=utf-8", "Not found");
    return;
  }

  const type = TYPES[extname(file)] ?? "application/octet-stream";
  send(response, 200, type, body);
}

// forward sends the call upstream with the service token instead of the
// placeholder the page sends.
async function forward(request, response, target, token) {
  const allowed = request.method === "GET" || request.method === "POST";
  if (!allowed) {
    send(response, 405, "text/plain; charset=utf-8", "Method not allowed");
    return;
  }

  const headers = { Authorization: `Bearer ${token}` };
  const body = request.method === "POST" ? await readBody(request) : undefined;
  if (body) {
    headers["Content-Type"] = "application/json";
  }

  const reply = await fetch(target, { method: request.method, headers, body });
  const type = reply.headers.get("content-type") ?? "application/json";
  send(response, reply.status, type, Buffer.from(await reply.arrayBuffer()));
}

async function readBody(request) {
  const chunks = [];
  let size = 0;

  for await (const chunk of request) {
    size += chunk.length;
    if (size > MAX_BODY) {
      throw new BodyTooLarge();
    }
    chunks.push(chunk);
  }

  return Buffer.concat(chunks);
}

class BodyTooLarge extends Error {}

function sendError(response, error) {
  if (response.headersSent) {
    response.destroy();
    return;
  }

  const status = error instanceof BodyTooLarge ? 413 : 502;
  const message = status === 413 ? "Request too large" : "Explorer service unreachable";
  send(response, status, "text/plain; charset=utf-8", message);
}

function send(response, status, type, body) {
  response.writeHead(status, { "Content-Type": type, "Cache-Control": "no-store" });
  response.end(body);
}
