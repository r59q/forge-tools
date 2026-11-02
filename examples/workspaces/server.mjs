import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { listFiles, readText, searchText } from './files.mjs';

const publicDirectory = new URL('./public/', import.meta.url);
const assets = { '/': ['index.html', 'text/html'], '/app.js': ['app.js', 'text/javascript'], '/style.css': ['style.css', 'text/css'] };

function reject(status, message) { throw Object.assign(new Error(message), { status }); }

// createExplorer serves the local demo UI. Service tokens stay on the server.
// The checkout volume must be mounted at the same absolute path as in workspaces.
export function createExplorer(config) {
  async function api(base, token, route, method = 'GET', body) {
    const response = await fetch(`${base}${route}`, {
      method, redirect: 'error', signal: AbortSignal.timeout(300_000),
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok) {
      const message = method === 'DELETE' && response.status === 400
        ? 'Removal refused. The workspace may contain changes; keep it or clean it up first.'
        : `Service request failed (${response.status}). Check that the services and connector clients are ready.`;
      reject(response.status, message);
    }
    return response.json();
  }
  const workspaceAPI = (route, method, body) => api(config.workspaceURL, config.workspaceToken, route, method, body);
  return createServer(async (req, res) => {
    res.setHeader('Cache-Control', 'no-store');
    res.setHeader('X-Content-Type-Options', 'nosniff');
    res.setHeader('Content-Security-Policy', "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'");
    const json = (value, status = 200) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); };
    try {
      // This is a loopback-only, single-user example, including in Compose.
      if (!/^(localhost|127\.0\.0\.1)(:\d+)?$/.test(req.headers.host || '') ||
          (req.headers.origin && req.headers.origin !== `http://${req.headers.host}`) || req.headers['sec-fetch-site'] === 'cross-site') {
        reject(403, 'Open this example through localhost.');
      }
      const url = new URL(req.url, `http://${req.headers.host}`);
      if (req.method === 'GET' && assets[url.pathname]) {
        const [name, type] = assets[url.pathname];
        res.setHeader('Content-Type', `${type}; charset=utf-8`);
        res.end(await readFile(new URL(name, publicDirectory))); return;
      }
      if (req.method === 'GET' && url.pathname === '/api/connections') {
        json(await api(config.connectorURL, config.connectorToken, '/connections')); return;
      }
      if (url.pathname === '/api/workspaces') {
        if (req.method === 'GET') { json(await workspaceAPI('/v1/workspaces')); return; }
        if (req.method === 'POST') {
          if (req.headers['content-type'] !== 'application/json') reject(415, 'Send JSON.');
          let bytes = 0;
          const chunks = [];
          for await (const chunk of req) { bytes += chunk.length; if (bytes > 8192) reject(413, 'Request too large.'); chunks.push(chunk); }
          let input;
          try { input = JSON.parse(Buffer.concat(chunks)); } catch { reject(400, 'Invalid JSON.'); }
          if (!input || typeof input.reference !== 'string' || input.reference.length > 1024 || typeof input.source !== 'string' || typeof input.repository !== 'string') reject(400, 'Choose a source, repository and reference.');
          const repository = input.source ? { source: input.source, path: input.repository } : { localPath: config.demoPath };
          json(await workspaceAPI('/v1/workspaces', 'POST', { repository, reference: input.reference })); return;
        }
      }
      const match = url.pathname.match(/^\/api\/workspaces\/([a-f0-9]{32})(?:\/(files|file|search|status|diff|retain))?$/);
      if (!match) reject(404, 'Not found.');
      const [, id, action] = match;
      const route = `/v1/workspaces/${id}`;
      if (req.method === 'DELETE' && !action) {
        // Deliberately never forwards discard=true: removal keeps Git's protections.
        json(await workspaceAPI(route, 'DELETE')); return;
      }
      if (req.method === 'POST' && action === 'retain') { json(await workspaceAPI(`${route}/retain`, 'POST', {})); return; }
      if (req.method !== 'GET') reject(405, 'Method not allowed.');
      if (action === 'status' || action === 'diff') { json(await workspaceAPI(`${route}/${action}`)); return; }
      const workspace = await workspaceAPI(route);
      if (!action) { json(workspace); return; }
      const root = path.join(config.dataDirectory, 'copies', id);
      if (workspace.location !== root) reject(409, 'The explorer and workspace service must share the same data path.');
      if (action === 'files') json(await listFiles(root));
      else if (action === 'file') json({ text: await readText(root, url.searchParams.get('path') || '') });
      else if (action === 'search') json(await searchText(root, url.searchParams.get('q') || ''));
      else reject(404, 'Not found.');
    } catch (err) {
      const status = err.status || (err.code === 'ENOENT' ? 404 : 502);
      json({ error: err.status ? err.message : status === 404 ? 'File not found.' : 'Could not read the workspace or reach a service.' }, status);
    }
  });
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (!process.env.FORGE_WORKSPACES_TOKEN || !process.env.FORGE_WORKSPACES_CONNECTOR_TOKEN) {
    console.error('Set FORGE_WORKSPACES_TOKEN and FORGE_WORKSPACES_CONNECTOR_TOKEN.');
    process.exitCode = 1;
  } else {
    const port = Number(process.env.PORT || 3030);
    createExplorer({
      workspaceURL: process.env.WORKSPACES_URL || 'http://127.0.0.1:8088',
      workspaceToken: process.env.FORGE_WORKSPACES_TOKEN,
      connectorURL: process.env.CONNECTOR_URL || 'http://127.0.0.1:8081',
      connectorToken: process.env.FORGE_WORKSPACES_CONNECTOR_TOKEN,
      dataDirectory: path.resolve(process.env.WORKSPACE_DATA_DIR || '/data'),
      demoPath: process.env.DEMO_PATH || '/data/demo',
    }).listen(port, process.env.HOST || '127.0.0.1', () => console.log(`Code explorer: http://127.0.0.1:${port}`));
  }
}
