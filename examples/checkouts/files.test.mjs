import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { listFiles, readText, searchText } from './files.mjs';

async function fixture(t) {
  const root = await mkdtemp(path.join(tmpdir(), 'explorer-files-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(path.join(root, 'src'));
  await writeFile(path.join(root, 'src', 'hello.js'), 'Hello, world\nhello again\n<script>alert(1)</script>');
  return root;
}

test('list, read and search checkout text without following links or exposing Git metadata', async t => {
  const root = await fixture(t);
  await mkdir(path.join(root, '.git'));
  await writeFile(path.join(root, '.git', 'config'), 'secret');
  await symlink('/etc/passwd', path.join(root, 'outside'));
  await symlink(path.join(root, '.git'), path.join(root, 'linked'));
  assert.deepEqual(await listFiles(root), { files: ['src/hello.js'], truncated: false });
  assert.match(await readText(root, 'src/hello.js'), /<script>/);
  const found = await searchText(root, 'HELLO');
  assert.deepEqual(found.matches.map(m => [m.path, m.line]), [['src/hello.js', 1], ['src/hello.js', 2]]);
  for (const name of ['../secret', '/etc/passwd', '.git/config', 'outside', 'linked/config', 'src/../../secret', 'src\\hello.js']) {
    await assert.rejects(readText(root, name), { status: 400 });
  }
  await assert.rejects(searchText(root, '  '), { status: 400 });
});

test('binary, invalid UTF-8 and large files are skipped; result caps are visible', async t => {
  const root = await fixture(t);
  await writeFile(path.join(root, 'binary'), Buffer.from([0, 1, 2]));
  await writeFile(path.join(root, 'invalid'), Buffer.from([0xff]));
  await writeFile(path.join(root, 'large'), 'x'.repeat(256 * 1024 + 1));
  await writeFile(path.join(root, 'matches'), 'needle\n'.repeat(101));
  for (const name of ['binary', 'invalid', 'large', 'src']) await assert.rejects(readText(root, name), { status: 400 });
  const found = await searchText(root, 'needle');
  assert.equal(found.matches.length, 100);
  assert.equal(found.truncated, true);
  assert.equal(found.skipped, 3);
  for (let i = 0; i < 2001; i++) await writeFile(path.join(root, `file-${i}`), '');
  const listing = await listFiles(root);
  assert.equal(listing.truncated, true);
  assert.ok(listing.files.length <= 2000);
});
