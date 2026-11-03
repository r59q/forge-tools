import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./modules.mjs";

const { History } = await load("src/model/history.ts");
const { Tabs } = await load("src/model/tabs.ts");
const { validateLocation, validateRead, wholeFile } = await load("src/model/location.ts");

const source = { id: "old", version: "root" };
const at = (path, version = "v1") => ({ source: "old", version, path, range: wholeFile() });

test("history moves back and forward and drops forward entries on push", () => {
  const history = new History();
  history.push(at("a"));
  history.push(at("b"));
  assert.equal(history.canGoBack, true);
  assert.equal(history.peek(-1).path, "a");

  history.go(-1);
  assert.equal(history.canGoForward, true);
  history.push(at("c"));
  assert.equal(history.canGoForward, false);
  assert.equal(history.length, 2);
});

test("history is bounded and returns copies", () => {
  const history = new History();
  for (let index = 0; index < 150; index++) history.push(at(`f${index}`));
  assert.equal(history.length, 100);

  history.peek(0).path = "changed";
  assert.equal(history.peek(0).path, "f149");
});

test("tabs keep one entry per path, are bounded and return copies", () => {
  const tabs = new Tabs();
  tabs.set(at("a", "v1"));
  tabs.set(at("a", "v2"));
  assert.equal(tabs.length, 1);
  assert.equal(tabs.all[0].version, "v2");

  for (let index = 0; index < 30; index++) tabs.set(at(`f${index}`));
  assert.equal(tabs.length, 20);
  assert.equal(tabs.indexOf("a"), -1);

  tabs.all[0].path = "changed";
  assert.equal(tabs.all[0].path, "f10");
  tabs.remove("f29");
  assert.equal(tabs.last().path, "f28");
});

test("locations must stay confined to their source", () => {
  for (const path of ["", "/abs", "a//b", "./a", "a/../b", ".git/config", "a\\b", "a\0"]) {
    assert.throws(() => validateLocation(source, at(path)), path);
  }
  assert.throws(() => validateLocation(source, { ...at("a"), source: "new" }));
  validateLocation(source, at("dir/.gitignore"));
});

test("reads must match the requested path and version", () => {
  const file = (path, version) => ({ location: at(path, version), text: "one" });
  validateRead(source, file("a", "v1"), at("a", ""));
  validateRead(source, file("a", "v1"), at("a", "v1"));
  assert.throws(() => validateRead(source, file("a", "v2"), at("a", "v1")), /changed/);
  assert.throws(() => validateRead(source, file("b", "v1"), at("a", "")), /changed/);
  assert.throws(() => validateRead(source, file("a", ""), at("a", "")), /changed/);
});
