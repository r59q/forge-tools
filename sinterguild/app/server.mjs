import { readFile } from "node:fs/promises";
import { createServer, request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { pipeline } from "node:stream/promises";

const assets = new URL("./dist/web/", import.meta.url);
const modules = /^\/(?:sinterguild\/(?:app|connection)|forge-chat\/frontend)\/src\/[a-z-]+\.(js|css)$/;
const chatRoute = /^\/api\/chat(\/v1\/(?:agents|chat-metadata)(?:\/.*)?)$/;
const types = {
  css: "text/css; charset=utf-8",
  html: "text/html; charset=utf-8",
  js: "text/javascript; charset=utf-8",
};

/** Serve SinterGuild and forward its API calls to the two configured services. */
export function createSinterGuild({ managementURL, chatURL }) {
  const management = new URL(managementURL);
  const chat = new URL(chatURL);
  return createServer((req, res) => {
    res.setHeader("Cache-Control", "no-store");
    res.setHeader("X-Content-Type-Options", "nosniff");
    res.setHeader("Referrer-Policy", "no-referrer");
    res.setHeader("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; " +
      "connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'");
    void dispatch(req, res, management, chat).catch(() => {
      if (res.headersSent) res.destroy();
      else res.writeHead(502).end("Service unavailable");
    });
  });
}

async function dispatch(req, res, management, chat) {
  const { pathname, search } = new URL(req.url, "http://localhost");
  const { method } = req;
  if (managementRoute(pathname, method)) {
    const path = pathname.slice("/api/management".length);
    return forward(req, res, new URL(path + search, management));
  }
  const match = chatRoute.exec(pathname);
  if (match) return forward(req, res, new URL(match[1] + search, chat));

  if (method === "GET" || method === "HEAD") {
    const file = pathname === "/" ? "/index.html" : pathname;
    if (file === "/index.html" || modules.test(file)) return serveAsset(req, res, file);
  }
  res.writeHead(404).end();
}

function managementRoute(path, method) {
  if (path === "/api/management/v1/connections") return method === "GET" || method === "POST";
  return path === "/api/management/v1/repositories" && method === "GET";
}

async function serveAsset(req, res, file) {
  const content = await readFile(new URL(file.slice(1), assets)).catch(error => {
    if (error.code !== "ENOENT") throw error;
  });
  if (!content) return res.writeHead(404).end();
  res.writeHead(200, { "Content-Type": types[file.split(".").pop()], "Content-Length": content.length });
  res.end(req.method === "HEAD" ? undefined : content);
}

function forward(req, res, target) {
  const headers = {};
  // Keep the public Host and browser origin metadata for management's cross-origin checks.
  for (const name of ["authorization", "content-type", "content-length", "accept", "host", "origin", "sec-fetch-site"]) {
    if (req.headers[name]) headers[name] = req.headers[name];
  }
  const send = target.protocol === "https:" ? httpsRequest : httpRequest;
  return new Promise((resolve, reject) => {
    const upstream = send(target, { method: req.method, headers }, reply => {
      if (reply.statusCode >= 300 && reply.statusCode < 400) {
        reply.destroy();
        reject(new Error("Unexpected upstream redirect"));
        return;
      }
      const type = reply.headers["content-type"];
      if (type) res.setHeader("Content-Type", type);
      res.writeHead(reply.statusCode);
      res.flushHeaders();
      resolve(pipeline(reply, res));
    });
    upstream.once("error", reject);
    // Relay records as they arrive, and stop the upstream stream on disconnect.
    res.once("close", () => {
      if (!res.writableFinished) upstream.destroy();
    });
    req.pipe(upstream);
  });
}

if (import.meta.main) {
  const server = createSinterGuild({
    managementURL: process.env.SINTERGUILD_MANAGEMENT_URL ?? "http://127.0.0.1:8083",
    chatURL: process.env.SINTERGUILD_CHAT_URL ?? "http://127.0.0.1:8097",
  });
  const host = process.env.SINTERGUILD_HOST ?? "127.0.0.1";
  const port = Number(process.env.SINTERGUILD_PORT ?? 8098);
  server.listen(port, host, () => {
    console.log(`SinterGuild listening on ${host}:${server.address().port}`);
  });
}
