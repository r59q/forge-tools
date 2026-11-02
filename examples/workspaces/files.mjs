import { constants } from 'node:fs';
import { lstat, readdir, open } from 'node:fs/promises';
import path from 'node:path';

const maxBytes = 256 * 1024;
const maxEntries = 2000;

function invalid(message) {
  return Object.assign(new Error(message), { status: 400 });
}

// checkedPath confines reads to a checkout, excluding Git metadata and symlinks.
export async function checkedPath(root, relative = '') {
  const parts = relative === '' ? [] : relative.split('/');
  if (path.isAbsolute(relative) || parts.some(p => !p || p === '.' || p === '..' || p === '.git' || p.includes('\\'))) {
    throw invalid('Choose a file inside the workspace.');
  }
  let target = root;
  for (const part of ['', ...parts]) {
    target = path.join(target, part);
    if ((await lstat(target)).isSymbolicLink()) throw invalid('Symlinks are skipped in this example.');
  }
  return target;
}

// listFiles returns a bounded list of regular files without following symlinks.
export async function listFiles(root) {
  await checkedPath(root);
  const files = [];
  let visited = 0;
  let truncated = false;
  async function walk(relative, depth) {
    if (depth > 20) { truncated = true; return; }
    const directory = await checkedPath(root, relative);
    const entries = await readdir(directory, { withFileTypes: true });
    entries.sort((a, b) => a.name.localeCompare(b.name));
    for (const entry of entries) {
      if (++visited > maxEntries) { truncated = true; return; }
      if (entry.name === '.git' || entry.isSymbolicLink()) continue;
      const name = relative ? `${relative}/${entry.name}` : entry.name;
      if (entry.isDirectory()) await walk(name, depth + 1);
      else if (entry.isFile()) files.push(name);
    }
  }
  await walk('', 0);
  return { files, truncated };
}

// readText reads small UTF-8 files; binary, large and special files are refused.
export async function readText(root, relative) {
  if (!relative) throw invalid('Choose a file.');
  const target = await checkedPath(root, relative);
  const file = await open(target, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
  try {
    const info = await file.stat();
    if (!info.isFile() || info.size > maxBytes) throw invalid('Only text files up to 256 KiB are shown.');
    const bytes = Buffer.alloc(maxBytes + 1);
    let size = 0;
    while (size < bytes.length) {
      const { bytesRead } = await file.read(bytes, size, bytes.length - size, size);
      if (!bytesRead) break;
      size += bytesRead;
    }
    if (size > maxBytes || bytes.subarray(0, size).includes(0)) throw invalid('Only text files up to 256 KiB are shown.');
    try { return new TextDecoder('utf-8', { fatal: true }).decode(bytes.subarray(0, size)); }
    catch { throw invalid('This file is not UTF-8 text.'); }
  } finally { await file.close(); }
}

// searchText finds literal, case-insensitive matches in a bounded file listing.
export async function searchText(root, query) {
  if (!query.trim() || query.length > 200) throw invalid('Enter 1–200 characters to search.');
  const listing = await listFiles(root);
  const matches = [];
  let skipped = 0;
  let scannedBytes = 0;
  for (const name of listing.files) {
    let text;
    try { text = await readText(root, name); }
    catch (err) {
      if (err.status === 400 || err.code === 'ENOENT') { skipped++; continue; }
      throw err;
    }
    scannedBytes += Buffer.byteLength(text);
    if (scannedBytes > 16 * 1024 * 1024) return { matches, skipped, truncated: true };
    for (const [line, value] of text.split('\n').entries()) {
      if (value.toLowerCase().includes(query.toLowerCase())) {
        matches.push({ path: name, line: line + 1, text: value.slice(0, 300) });
        if (matches.length === 100) return { matches, skipped, truncated: true };
      }
    }
  }
  return { matches, skipped, truncated: listing.truncated };
}
