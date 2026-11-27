import assert from "node:assert/strict"
import test from "node:test"

import { verifyVault } from "./verify.mjs"

const config = {
  address: "https://vault.example",
  username: "test/user",
  password: "synthetic-password",
}
const login = { auth: { client_token: "synthetic-token" } }
const prod = {
  ssh_username: "synthetic-user",
  ssh_private_key: "synthetic-key",
  hostname: "synthetic-host",
}
const dotted = {
  "prod.ssh_username": "synthetic-user",
  "prod.ssh_private_key": "synthetic-key",
  "prod.hostname": "synthetic-host",
}

function vaultResponses(...responses) {
  return async () => responses.shift()
}

test("logs in and reads only the configuration path", async () => {
  const calls = []
  const respond = vaultResponses(Response.json(login), Response.json({ data: { data: dotted } }))
  await verifyVault(config, async (url, options) => {
    calls.push({ url, ...options })
    return respond()
  })
  assert.deepEqual(calls.map(({ url, method, redirect }) => [url, method, redirect]), [
    ["https://vault.example/v1/auth/userpass/login/test%2Fuser", "POST", "manual"],
    ["https://vault.example/v1/kv/data/forge-tools", "GET", "manual"],
  ])
  assert.deepEqual(JSON.parse(calls[0].body), { password: "synthetic-password" })
  assert.deepEqual(calls[1].headers, { "X-Vault-Token": "synthetic-token" })
})

test("accepts nested production fields", async () => {
  const request = vaultResponses(Response.json(login), Response.json({ data: { data: { prod } } }))
  await verifyVault(config, request)
})

test("sanitizes connection failures", async () => {
  await assert.rejects(verifyVault(config, async () => {
    throw new Error("synthetic-sensitive-detail")
  }), { message: "Vault connection failed during authentication." })
})

test("fails authentication on a redirect or rejected password", async () => {
  for (const status of [307, 403]) {
    const request = vaultResponses(new Response("synthetic-sensitive-detail", { status }))
    await assert.rejects(verifyVault(config, request), { message: "Vault authentication failed." })
  }
})

test("sanitizes denied reads and invalid JSON", async () => {
  for (const [status, message] of [
    [403, "Vault read failed."],
    [200, "Vault read returned invalid JSON."],
  ]) {
    const request = vaultResponses(Response.json(login), new Response("synthetic-sensitive-detail", { status }))
    await assert.rejects(verifyVault(config, request), { message })
  }
})

test("requires every SSH field to be a nonblank string", async () => {
  for (const field of Object.keys(prod)) {
    for (const value of [undefined, "", "  ", 1]) {
      const key = `prod.${field}`
      for (const data of [{ prod: { ...prod, [field]: value } }, { ...dotted, [key]: value }]) {
        const request = vaultResponses(Response.json(login), Response.json({ data: { data } }))
        await assert.rejects(verifyVault(config, request), {
          message: `Vault field ${key} is missing or empty.`,
        })
      }
    }
  }
})

test("validates an explicit dotted field before its nested equivalent", async () => {
  const data = { ...dotted, prod, "prod.ssh_username": "" }
  const request = vaultResponses(Response.json(login), Response.json({ data: { data } }))
  await assert.rejects(verifyVault(config, request), {
    message: "Vault field prod.ssh_username is missing or empty.",
  })
})
