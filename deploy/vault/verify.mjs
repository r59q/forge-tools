import { pathToFileURL } from "node:url"

class VerificationError extends Error {}

async function requestJSON(request, url, phase, options) {
  let response
  try {
    response = await request(url, {
      ...options,
      redirect: "manual",
      signal: AbortSignal.timeout(15_000),
    })
  } catch {
    throw new VerificationError(`Vault connection failed during ${phase}.`)
  }

  if (response.status !== 200) {
    await response.body?.cancel().catch(() => {})
    throw new VerificationError(`Vault ${phase} failed.`)
  }
  try {
    return await response.json()
  } catch (error) {
    if (error instanceof SyntaxError) {
      throw new VerificationError(`Vault ${phase} returned invalid JSON.`)
    }
    throw new VerificationError(`Vault connection failed during ${phase}.`)
  }
}

function vaultOrigin(address) {
  try {
    const url = new URL(address)
    if (url.protocol === "https:" && !url.username && !url.password &&
        url.pathname === "/" && !url.search && !url.hash) {
      return url.origin
    }
  } catch {
    // Report a fixed diagnostic rather than exposing the address.
  }
  throw new VerificationError("Vault address must be an HTTPS origin.")
}

/** Authenticate with userpass and read the single KV v2 configuration path. */
export async function verifyVault({ address, username, password }, request = fetch) {
  if (!address || !username || !password) {
    throw new VerificationError("Vault address or login configuration is missing.")
  }

  const origin = vaultOrigin(address)
  const login = await requestJSON(
    request, `${origin}/v1/auth/userpass/login/${encodeURIComponent(username)}`,
    "authentication",
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ password }),
    },
  )
  const token = login?.auth?.client_token
  if (typeof token !== "string" || !token.trim()) {
    throw new VerificationError("Vault authentication returned no usable token.")
  }

  const result = await requestJSON(request, `${origin}/v1/kv/data/forge-tools`, "read", {
    method: "GET",
    headers: { "X-Vault-Token": token },
  })
  const data = result?.data?.data
  for (const field of ["ssh_username", "ssh_private_key", "hostname"]) {
    const key = `prod.${field}`
    const value = data && Object.hasOwn(data, key) ? data[key] : data?.prod?.[field]
    if (typeof value !== "string" || !value.trim()) {
      throw new VerificationError(`Vault field ${key} is missing or empty.`)
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await verifyVault({
      address: process.env.VAULT_ADDR,
      username: process.env.VAULT_USERNAME,
      password: process.env.VAULT_PASSWORD,
    })
    console.log("Vault verification passed.")
  } catch (error) {
    console.error(error instanceof VerificationError ? error.message : "Vault verification failed unexpectedly.")
    process.exitCode = 1
  }
}
