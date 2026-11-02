const $ = id => document.getElementById(id);
let checkouts = [];
let selected;
let busy = false;

async function api(route, method = 'GET', body) {
  const response = await fetch(`/api${route}`, {
    method, headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || 'Request failed.');
  return data;
}
function notice(text, error = false) { $('notice').textContent = text; $('notice').classList.toggle('error', error); }
function controls() {
  for (const element of document.querySelectorAll('button, input, select')) element.disabled = busy || (element.hasAttribute('data-checkout') && !selected);
  $('repository').disabled = busy || !$('source').value;
  $('repository').required = !!$('source').value;
}
async function run(action) {
  if (busy) return;
  busy = true; controls(); notice('Working…');
  try { await action(); } catch (err) { notice(err.message, true); }
  finally { busy = false; controls(); }
}
function checkoutRoute(action = '') { return `/checkouts/${selected.id}${action ? `/${action}` : ''}`; }
function show(title, text) { $('file-title').textContent = title; $('code').textContent = text; }
function link(container, label, action) {
  const button = document.createElement('button');
  button.type = 'button'; button.textContent = label; button.addEventListener('click', () => run(action));
  container.append(button);
}
async function openFile(name, line = 0) {
  const data = await api(`${checkoutRoute('file')}?path=${encodeURIComponent(name)}`);
  show(line ? `${name}:${line}` : name, data.text.split('\n').map((text, i) => `${String(i + 1).padStart(4)}  ${text}`).join('\n'));
  $('code').scrollTop = Math.max(0, line - 4) * 22;
  notice('Reading the checked-out file from the shared checkout volume.');
}
async function choose() {
  selected = checkouts.find(w => w.id === $('checkout').value);
  $('files').replaceChildren(); $('results').replaceChildren(); $('results-title').hidden = true;
  show('No file selected', 'Choose a file to read its contents.');
  if (!selected) { $('metadata').textContent = 'Create a checkout to begin.'; notice(''); return; }
  $('metadata').textContent = `${selected.reference} → ${selected.revision || 'unresolved'} · ${selected.state}\n${selected.location}`;
  const data = await api(checkoutRoute('files'));
  for (const name of data.files) link($('files'), name, () => openFile(name));
  notice(`${data.files.length} files${data.truncated ? ' (listing limited)' : ''}. Select one or search its contents.`);
}
async function refresh(id = selected?.id) {
  checkouts = (await api('/checkouts')).checkouts || [];
  $('checkout').replaceChildren();
  for (const checkout of checkouts) {
    const option = document.createElement('option'); option.value = checkout.id;
    option.textContent = `${checkout.repository.path || 'Local demo'} · ${checkout.reference} · ${checkout.id.slice(0, 8)} · ${checkout.state}`;
    $('checkout').append(option);
  }
  if (checkouts.some(w => w.id === id)) $('checkout').value = id;
  if (!checkouts.length) $('checkout').append(new Option('No checkouts yet', ''));
  await choose();
}
$('source').addEventListener('change', controls);
$('checkout').addEventListener('change', () => run(choose));
$('refresh').addEventListener('click', () => run(() => refresh()));
$('create-form').addEventListener('submit', event => {
  event.preventDefault(); run(async () => {
    const checkout = await api('/checkouts', 'POST', { source: $('source').value, repository: $('repository').value, reference: $('reference').value });
    await refresh(checkout.id);
  });
});
$('search-form').addEventListener('submit', event => {
  event.preventDefault(); run(async () => {
    const data = await api(`${checkoutRoute('search')}?q=${encodeURIComponent($('query').value)}`);
    $('results').replaceChildren(); $('results-title').hidden = false;
    for (const match of data.matches) link($('results'), `${match.path}:${match.line} — ${match.text}`, () => openFile(match.path, match.line));
    notice(`${data.matches.length} matches${data.truncated ? ' (search limited)' : ''}; ${data.skipped} files skipped.`);
  });
});
$('retain').addEventListener('click', () => run(async () => { await api(checkoutRoute('retain'), 'POST', {}); await refresh(); notice('Retained. This checkout remains until you explicitly remove it.'); }));
$('remove').addEventListener('click', () => run(async () => { await api(checkoutRoute(), 'DELETE'); selected = undefined; await refresh(); notice('Checkout removed. Other checkouts are unchanged.'); }));
$('status').addEventListener('click', () => run(async () => {
  const [status, diff] = await Promise.all([api(checkoutRoute('status')), api(checkoutRoute('diff'))]);
  const decode = value => new TextDecoder().decode(Uint8Array.from(atob(value || ''), c => c.charCodeAt(0)));
  const files = (status.files || []).map(f => `${f.index}${f.worktree} ${f.path}`).join('\n');
  show('Checkout status & diff', `HEAD ${status.revision}\nBranch: ${status.branch || '(detached)'}\n\n${files || 'Working copy is clean.'}\n\nSTAGED\n${decode(diff.staged) || '(none)'}\n\nUNSTAGED\n${decode(diff.unstaged) || '(none)'}`);
  notice('Status and diffs come from forge-checkouts.');
}));
run(async () => {
  await refresh();
  try {
    const data = await api('/connections');
    for (const connection of data.connections || []) {
      if (connection.status !== 'ok') continue;
      $('source').append(new Option(`${connection.name || connection.id} (${connection.kind})`, connection.id));
    }
  } catch {
    $('connections-note').textContent = 'Local demo is ready. Run the connector setup from the README to enable connected repositories, then reload.';
  }
});
