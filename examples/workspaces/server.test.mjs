import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer, request } from 'node:http';
import { once } from 'node:events';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { createExplorer } from './server.mjs';

async function listen(t, server) {
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  return `http://127.0.0.1:${server.address().port}`;
}

test('exploration uses the shared checkout and forwards only supported workspace operations', async t => {
  const root = await mkdtemp(path.join(tmpdir(), 'explorer-server-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const id = 'a'.repeat(32);
  const location = path.join(root, 'copies', id);
  await mkdir(location, { recursive: true });
  await writeFile(path.join(location, 'hello.js'), 'hello from checkout\n');
  const record = { id, location, reference: 'main', state: 'ready', repository: { source: 'gh', path: 'owner/repo' } };
  const calls = [];
  const upstream = await listen(t, createServer(async (req, res) => {
    assert.equal(req.headers.authorization, req.url === '/connections' ? 'Bearer connector-only' : 'Bearer workspace-only');
    let data = ''; for await (const chunk of req) data += chunk;
    calls.push({ method: req.method, path: req.url, body: data ? JSON.parse(data) : undefined });
    res.setHeader('Content-Type', 'application/json');
    if (req.method === 'DELETE') { res.writeHead(400); res.end('{"message":"private-upstream-detail"}'); return; }
    if (req.url === '/connections') res.end(JSON.stringify({ connections: [{ id: 'gh', status: 'ok' }] }));
    else if (req.url.endsWith('/status')) res.end('{"files":[{"path":"hello.js","worktree":"M"}]}');
    else if (req.url.endsWith('/diff')) res.end('{"unstaged":"K2hlbGxv"}');
    else if (req.url.endsWith('/retain')) res.end(JSON.stringify({ ...record, state: 'retained' }));
    else if (req.url === '/v1/workspaces' && req.method === 'GET') res.end(JSON.stringify({ workspaces: [record] }));
    else res.end(JSON.stringify(record));
  }));
  const base = await listen(t, createExplorer({ workspaceURL: upstream, workspaceToken: 'workspace-only', connectorURL: upstream, connectorToken: 'connector-only', dataDirectory: root, demoPath: '/data/demo' }));
  const api = async (route, options) => { const r = await fetch(base + route, options); return { status: r.status, data: await r.json() }; };
  assert.equal((await api('/api/connections')).data.connections[0].id, 'gh');
  for (const source of ['', 'gh']) {
    const created = await api('/api/workspaces', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ source, repository: 'owner/repo', reference: 'v1', localPath: '/etc' }) });
    assert.equal(created.status, 200);
    assert.deepEqual(calls.at(-1).body.repository, source ? { source, path: 'owner/repo' } : { localPath: '/data/demo' });
  }
  assert.equal((await api('/api/workspaces')).data.workspaces.length, 1);
  assert.deepEqual((await api(`/api/workspaces/${id}/files`)).data.files, ['hello.js']);
  assert.equal((await api(`/api/workspaces/${id}/file?path=hello.js`)).data.text, 'hello from checkout\n');
  assert.equal((await api(`/api/workspaces/${id}/search?q=checkout`)).data.matches.length, 1);
  assert.equal((await api(`/api/workspaces/${id}/status`)).data.files[0].path, 'hello.js');
  assert.equal((await api(`/api/workspaces/${id}/diff`)).data.unstaged, 'K2hlbGxv');
  assert.equal((await api(`/api/workspaces/${id}/retain`, { method: 'POST' })).data.state, 'retained');
  const removed = await api(`/api/workspaces/${id}?discard=true`, { method: 'DELETE' });
  assert.equal(removed.status, 400);
  assert.match(removed.data.error, /Removal refused/);
  assert.equal(calls.at(-1).path, `/v1/workspaces/${id}`);
  assert.equal((await api(`/api/workspaces/${id}/file?path=../demo`)).status, 400);
  const before = calls.length;
  assert.equal((await api('/api/workspaces', { headers: { Origin: 'https://attacker.example' } })).status, 403);
  const rebound = await new Promise((resolve, reject) => {
    request(base + '/api/workspaces', { headers: { Host: 'attacker.example' } }, res => { res.resume(); resolve(res.statusCode); }).on('error', reject).end();
  });
  assert.equal(rebound, 403);
  assert.equal((await api('/api/workspaces', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: 'null' })).status, 400);
  assert.equal(calls.length, before);
  for (const route of ['/', '/app.js', '/style.css']) {
    const response = await fetch(base + route);
    assert.equal(response.status, 200);
    assert.ok(response.headers.get('content-security-policy').includes("script-src 'self'"));
    assert.doesNotMatch(await response.text(), /workspace-only|connector-only/);
  }
  record.location = '/etc';
  assert.equal((await api(`/api/workspaces/${id}/files`)).status, 409);
});
