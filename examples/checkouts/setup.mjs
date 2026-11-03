import { randomBytes } from 'node:crypto';
import { chmod, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const names = [
  'FORGE_BROKER_KEY',
  'FORGE_BROKER_ADMIN_TOKEN',
  'FORGE_PROXY_TOKEN',
  'FORGE_CHECKOUTS_TOKEN',
  'FORGE_CHECKOUTS_CONNECTOR_TOKEN',
];

function fail(message) {
  throw Object.assign(new Error(message), { display: true });
}

function endpoint(value, name) {
  let url;
  try {
    url = new URL(value);
  } catch {
    fail(`${name} must be a full http or https URL.`);
  }
  if (
    !['http:', 'https:'].includes(url.protocol) ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    fail(`${name} must be an http or https URL without credentials, query or fragment.`);
  }
  return url.toString().replace(/\/$/, '');
}

// initialize creates missing demo secrets in .env without replacing existing ones.
export async function initialize(directory = '.') {
  const file = path.join(directory, '.env');
  let text = await readFile(file, 'utf8').catch(err => {
    if (err.code === 'ENOENT') return '';
    throw err;
  });
  if (text && !text.endsWith('\n')) text += '\n';
  for (const name of names) {
    if (!new RegExp(`^${name}=.+$`, 'm').test(text)) {
      text = text.replace(new RegExp(`^${name}=.*\\n?`, 'm'), '');
      text += `${name}=${randomBytes(32).toString(name === 'FORGE_BROKER_KEY' ? 'base64' : 'hex')}\n`;
    }
  }
  await writeFile(file, text, { mode: 0o600 });
  await chmod(file, 0o600);
}

function brokerRequest(env) {
  const base = env.BROKER_URL || 'http://127.0.0.1:8089';

  return async (route, token, method = 'GET', body) => {
    const response = await fetch(`${base}${route}`, {
      method,
      redirect: 'error',
      signal: AbortSignal.timeout(30_000),
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    });

    if (!response.ok) {
      throw Object.assign(new Error(`Broker request failed (${response.status}).`), {
        status: response.status,
      });
    }

    return response.json();
  };
}

async function registerClients(env, request, admin) {
  const clients = [
    ['example-proxy', env.FORGE_PROXY_TOKEN, true],
    ['example-checkouts', env.FORGE_CHECKOUTS_CONNECTOR_TOKEN, false],
  ];

  for (const [name, token, canIssueTokens] of clients) {
    if (!token) {
      fail('Load .env with node --env-file=.env.');
    }

    try {
      await request('/v1/client/connections', token);
    } catch (err) {
      if (err.status !== 401) {
        throw err;
      }

      await admin('/v1/clients', 'POST', { name, token, canIssueTokens, allConnections: true });
    }
  }

  return 'Connector clients are ready.';
}

function connectionEndpoints(env, command, kind) {
  const defaults = {
    github: 'https://api.github.com',
    gitlab: 'https://gitlab.com/api/v4',
  };

  if (command !== 'connect' || !['github', 'gitlab', 'forgejo'].includes(kind)) {
    fail('Use register or connect github|gitlab|forgejo.');
  }
  if (!env.FORGE_TOKEN) {
    fail('Set FORGE_TOKEN to a token for the selected forge.');
  }
  if (kind === 'forgejo' && !env.FORGE_API_URL) {
    fail('Forgejo requires FORGE_API_URL=https://<your-forgejo-host>/api/v1.');
  }

  const baseUrl = endpoint(env.FORGE_API_URL || defaults[kind], 'FORGE_API_URL');
  const gitBaseUrl = env.FORGE_GIT_URL ? endpoint(env.FORGE_GIT_URL, 'FORGE_GIT_URL') : '';

  return { baseUrl, gitBaseUrl };
}

async function addConnection(admin, env, forge, kind, baseUrl) {
  try {
    return await admin('/v1/connections', 'POST', {
      forgeId: forge.id,
      name: 'Explorer account',
      credential: env.FORGE_TOKEN,
    });
  } catch (err) {
    if (err.status !== 400) {
      throw err;
    }

    const hint = kind === 'forgejo'
      ? ''
      : ' For Forgejo, use connect forgejo and set FORGE_API_URL to its /api/v1 endpoint.';

    fail(
      `The broker could not validate the token for ${kind} at ${baseUrl} (HTTP 400). ` +
      `Check the token and selected forge.${hint}`,
    );
  }
}

// configure registers the example's clients, or adds a connection using a token
// supplied in FORGE_TOKEN. Only the connector broker receives forge credentials.
export async function configure(env, command, kind = 'github') {
  const request = brokerRequest(env);

  if (!env.FORGE_BROKER_ADMIN_TOKEN) {
    fail('Load .env with node --env-file=.env.');
  }

  const admin = (route, method, body) => request(route, env.FORGE_BROKER_ADMIN_TOKEN, method, body);

  if (command === 'register') {
    return registerClients(env, request, admin);
  }

  const { baseUrl, gitBaseUrl } = connectionEndpoints(env, command, kind);
  const existing = (await admin('/v1/forges')).forges || [];
  const forge = existing.find(f => f.kind === kind && f.baseUrl === baseUrl) ||
    await admin('/v1/forges', 'POST', { kind, baseUrl, gitBaseUrl, name: 'Explorer example' });
  const connection = await addConnection(admin, env, forge, kind, baseUrl);

  return `Connection ${connection.id} created. Reload the explorer to select it.`;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (!process.argv[2]) {
      await initialize();
      console.log('Secrets ready in .env. Start the services with docker compose up --build -d.');
    } else console.log(await configure(process.env, process.argv[2], process.argv[3]));
  } catch (err) {
    console.error(
      err.status || err.display
        ? err.message
        : 'Setup failed. Check the command, required environment variables, and broker availability.',
    );
    process.exitCode = 1;
  }
}
