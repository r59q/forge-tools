import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { listFiles, readText, searchText } from './files.mjs';

const publicDirectory = new URL('./public/', import.meta.url);
const assets = {
  '/': ['index.html', 'text/html'],
  '/app.js': ['app.js', 'text/javascript'],
  '/style.css': ['style.css', 'text/css'],
};
const securityPolicy =
  "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
  "object-src 'none'; base-uri 'none'; frame-ancestors 'none'";

function reject(status, message) {
  throw Object.assign(new Error(message), { status });
}

function sendJSON(res, value, status = 200) {
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(value));
}

function setSecurityHeaders(res) {
  res.setHeader('Cache-Control', 'no-store');
  res.setHeader('X-Content-Type-Options', 'nosniff');
  res.setHeader('Content-Security-Policy', securityPolicy);
}

function requireLocalRequest(req) {
  const host = req.headers.host || '';
  const localHost = /^(localhost|127\.0\.0\.1)(:\d+)?$/.test(host);
  const otherOrigin = req.headers.origin && req.headers.origin !== `http://${host}`;
  const crossSite = req.headers['sec-fetch-site'] === 'cross-site';

  if (!localHost || otherOrigin || crossSite) {
    reject(403, 'Open this example through localhost.');
  }
}

async function callService(base, token, route, method = 'GET', body) {
  const response = await fetch(`${base}${route}`, {
    method,
    redirect: 'error',
    signal: AbortSignal.timeout(300_000),
    headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });

  if (!response.ok) {
    if (method === 'DELETE' && response.status === 400) {
      reject(
        response.status,
        'Removal refused. The checkout may contain changes; keep it or clean it up first.',
      );
    }
    reject(
      response.status,
      `Service request failed (${response.status}). Check that the services and connector clients are ready.`,
    );
  }

  return response.json();
}

async function readCreateRequest(req) {
  if (req.headers['content-type'] !== 'application/json') {
    reject(415, 'Send JSON.');
  }

  let bytes = 0;
  const chunks = [];
  for await (const chunk of req) {
    bytes += chunk.length;
    if (bytes > 8192) {
      reject(413, 'Request too large.');
    }
    chunks.push(chunk);
  }

  let input;
  try {
    input = JSON.parse(Buffer.concat(chunks));
  } catch {
    reject(400, 'Invalid JSON.');
  }

  if (
    !input ||
    typeof input.reference !== 'string' ||
    input.reference.length > 1024 ||
    typeof input.source !== 'string' ||
    typeof input.repository !== 'string'
  ) {
    reject(400, 'Choose a source, repository and reference.');
  }

  return input;
}

async function serveAsset(res, asset) {
  const [name, type] = asset;
  res.setHeader('Content-Type', `${type}; charset=utf-8`);
  res.end(await readFile(new URL(name, publicDirectory)));
}

async function handleCheckoutList(req, res, config, checkoutAPI) {
  if (req.method === 'GET') {
    sendJSON(res, await checkoutAPI('/v1/checkouts'));
    return true;
  }

  if (req.method === 'POST') {
    const input = await readCreateRequest(req);
    const repository = input.source
      ? { source: input.source, path: input.repository }
      : { localPath: config.demoPath };
    const checkout = await checkoutAPI('/v1/checkouts', 'POST', {
      repository,
      reference: input.reference,
    });
    sendJSON(res, checkout);
    return true;
  }

  return false;
}

async function readCheckoutFiles(res, action, root, url) {
  if (action === 'files') {
    sendJSON(res, await listFiles(root));
  } else if (action === 'file') {
    const text = await readText(root, url.searchParams.get('path') || '');
    sendJSON(res, { text });
  } else if (action === 'search') {
    sendJSON(res, await searchText(root, url.searchParams.get('q') || ''));
  } else {
    reject(404, 'Not found.');
  }
}

async function handleCheckout(req, res, url, config, checkoutAPI) {
  const match = url.pathname.match(
    /^\/api\/checkouts\/([a-f0-9]{32})(?:\/(files|file|search|status|diff|retain))?$/,
  );
  if (!match) {
    reject(404, 'Not found.');
  }

  const [, id, action] = match;
  const route = `/v1/checkouts/${id}`;

  if (req.method === 'DELETE' && !action) {
    // Never forward discard=true; preserve Git's removal protections.
    sendJSON(res, await checkoutAPI(route, 'DELETE'));
    return;
  }
  if (req.method === 'POST' && action === 'retain') {
    sendJSON(res, await checkoutAPI(`${route}/retain`, 'POST', {}));
    return;
  }
  if (req.method !== 'GET') {
    reject(405, 'Method not allowed.');
  }

  if (action === 'status' || action === 'diff') {
    sendJSON(res, await checkoutAPI(`${route}/${action}`));
    return;
  }

  const checkout = await checkoutAPI(route);
  if (!action) {
    sendJSON(res, checkout);
    return;
  }

  const root = path.join(config.dataDirectory, 'copies', id);
  if (checkout.location !== root) {
    reject(409, 'The explorer and checkout service must share the same data path.');
  }
  await readCheckoutFiles(res, action, root, url);
}

async function handleRequest(req, res, config, checkoutAPI) {
  requireLocalRequest(req);
  const url = new URL(req.url, `http://${req.headers.host}`);

  if (req.method === 'GET' && assets[url.pathname]) {
    await serveAsset(res, assets[url.pathname]);
    return;
  }
  if (req.method === 'GET' && url.pathname === '/api/connections') {
    const connections = await callService(config.connectorURL, config.connectorToken, '/connections');
    sendJSON(res, connections);
    return;
  }
  if (url.pathname === '/api/checkouts') {
    const handled = await handleCheckoutList(req, res, config, checkoutAPI);
    if (handled) {
      return;
    }
  }

  await handleCheckout(req, res, url, config, checkoutAPI);
}

function handleError(res, err) {
  const status = err.status || (err.code === 'ENOENT' ? 404 : 502);
  let message = 'Could not read the checkout or reach a service.';

  if (err.status) {
    message = err.message;
  } else if (status === 404) {
    message = 'File not found.';
  }

  sendJSON(res, { error: message }, status);
}

// createExplorer serves the local demo UI. Service tokens stay on the server.
// The checkout volume must be mounted at the same absolute path as in checkouts.
export function createExplorer(config) {
  const checkoutAPI = (route, method, body) =>
    callService(config.checkoutURL, config.checkoutToken, route, method, body);

  return createServer(async (req, res) => {
    setSecurityHeaders(res);
    try {
      await handleRequest(req, res, config, checkoutAPI);
    } catch (err) {
      handleError(res, err);
    }
  });
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (!process.env.FORGE_CHECKOUTS_TOKEN || !process.env.FORGE_CHECKOUTS_CONNECTOR_TOKEN) {
    console.error('Set FORGE_CHECKOUTS_TOKEN and FORGE_CHECKOUTS_CONNECTOR_TOKEN.');
    process.exitCode = 1;
  } else {
    const port = Number(process.env.PORT || 3030);
    createExplorer({
      checkoutURL: process.env.CHECKOUTS_URL || 'http://127.0.0.1:8088',
      checkoutToken: process.env.FORGE_CHECKOUTS_TOKEN,
      connectorURL: process.env.CONNECTOR_URL || 'http://127.0.0.1:8081',
      connectorToken: process.env.FORGE_CHECKOUTS_CONNECTOR_TOKEN,
      dataDirectory: path.resolve(process.env.CHECKOUT_DATA_DIR || '/data'),
      demoPath: process.env.DEMO_PATH || '/data/demo',
    }).listen(port, process.env.HOST || '127.0.0.1', () =>
      console.log(`Code explorer: http://127.0.0.1:${port}`),
    );
  }
}
