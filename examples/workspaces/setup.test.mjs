import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, stat, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { initialize, configure } from './setup.mjs';

test('setup preserves existing secrets and writes a private env file', async t => {
  const root = await mkdtemp(path.join(tmpdir(), 'explorer-setup-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  await initialize(root);
  const first = await readFile(path.join(root, '.env'), 'utf8');
  assert.match(first, /^FORGE_BROKER_KEY=.+$/m);
  assert.match(first, /^FORGE_WORKSPACES_TOKEN=[a-f0-9]{64}$/m);
  assert.equal((await stat(path.join(root, '.env'))).mode & 0o777, 0o600);
  await initialize(root);
  assert.equal(await readFile(path.join(root, '.env'), 'utf8'), first);
});

test('setup registers distinct client grants idempotently and sends forge tokens only to the broker', async t => {
  const clients = new Map(); const calls = [];
  const server = createServer(async (req, res) => {
    let text = ''; for await (const chunk of req) text += chunk;
    const body = text ? JSON.parse(text) : undefined;
    calls.push({ path: req.url, body });
    res.setHeader('Content-Type', 'application/json');
    if (req.url === '/v1/client/connections') {
      res.writeHead(clients.has(req.headers.authorization) ? 200 : 401); res.end('{}'); return;
    }
    assert.equal(req.headers.authorization, 'Bearer admin-only');
    if (req.url === '/v1/connections' && body.credential === 'invalid-secret') {
      res.writeHead(400); res.end('{"message":"invalid-secret private-upstream-detail"}'); return;
    }
    if (req.url === '/v1/clients') { clients.set(`Bearer ${body.token}`, body); res.end('{}'); }
    else if (req.url === '/v1/forges') res.end(req.method === 'GET' ? '{"forges":[]}' : '{"id":"forge"}');
    else res.end('{"id":"connection"}');
  });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  t.after(() => new Promise(resolve => { server.closeAllConnections(); server.close(resolve); }));
  const env = { BROKER_URL: `http://127.0.0.1:${server.address().port}`, FORGE_BROKER_ADMIN_TOKEN: 'admin-only', FORGE_PROXY_TOKEN: 'proxy-only', FORGE_WORKSPACES_CONNECTOR_TOKEN: 'client-only', FORGE_TOKEN: 'forge-only' };
  await configure(env, 'register'); await configure(env, 'register');
  assert.equal(calls.filter(c => c.path === '/v1/clients').length, 2);
  assert.equal(clients.get('Bearer proxy-only').canIssueTokens, true);
  assert.equal(clients.get('Bearer client-only').canIssueTokens, false);
  const result = await configure(env, 'connect', 'github');
  assert.equal(calls.at(-1).body.credential, 'forge-only');
  assert.doesNotMatch(result, /forge-only|admin-only/);
  await assert.rejects(configure({ ...env, FORGE_TOKEN: 'invalid-secret' }, 'connect', 'github'), err => {
    assert.equal(err.display, true);
    assert.match(err.message, /github at https:\/\/api.github.com/);
    assert.match(err.message, /connect forgejo/);
    assert.doesNotMatch(err.message, /invalid-secret|private-upstream-detail/);
    return true;
  });
  await configure({ ...env, FORGE_API_URL: 'https://forge.example/api/v1/' }, 'connect', 'forgejo');
  assert.equal(calls.at(-2).body.kind, 'forgejo');
  assert.equal(calls.at(-2).body.baseUrl, 'https://forge.example/api/v1');
  assert.equal(calls.at(-1).body.credential, 'forge-only');
});

test('CLI explains missing Forgejo API URL instead of hiding validation errors', () => {
  const result = spawnSync(process.execPath, [fileURLToPath(new URL('./setup.mjs', import.meta.url)), 'connect', 'forgejo'], {
    env: { FORGE_BROKER_ADMIN_TOKEN: 'admin-only', FORGE_TOKEN: 'forge-only' }, encoding: 'utf8',
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /Forgejo requires FORGE_API_URL=https:\/\//);
  assert.doesNotMatch(result.stderr, /forge-only|admin-only|Setup failed/);
});

test('reject invalid or credential-bearing API URLs before sending credentials', async () => {
  const env = { FORGE_BROKER_ADMIN_TOKEN: 'admin-only', FORGE_TOKEN: 'forge-only' };
  for (const url of ['forge.example', 'file:///tmp/example', 'https://user:secret@forge.example/api/v1', 'https://forge.example/api/v1?token=secret']) {
    await assert.rejects(configure({ ...env, FORGE_API_URL: url }, 'connect', 'forgejo'), err => {
      assert.match(err.message, /FORGE_API_URL must/);
      assert.doesNotMatch(err.message, /secret|forge-only/);
      return true;
    });
  }
});
