const $ = id => document.getElementById(id);
let workspaces = [];
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
  for (const element of document.querySelectorAll('button, input, select')) element.disabled = busy || (element.hasAttribute('data-workspace') && !selected);
  $('repository').disabled = busy || !$('source').value;
  $('repository').required = !!$('source').value;
}
async function run(action) {
  if (busy) return;
  busy = true; controls(); notice('Working…');
  try { await action(); } catch (err) { notice(err.message, true); }
  finally { busy = false; controls(); }
}
function workspaceRoute(action = '') { return `/workspaces/${selected.id}${action ? `/${action}` : ''}`; }
function show(title, text) { $('file-title').textContent = title; $('code').textContent = text; }
function link(container, label, action) {
  const button = document.createElement('button');
  button.type = 'button'; button.textContent = label; button.addEventListener('click', () => run(action));
  container.append(button);
}
async function openFile(name, line = 0) {
  const data = await api(`${workspaceRoute('file')}?path=${encodeURIComponent(name)}`);
  show(line ? `${name}:${line}` : name, data.text.split('\n').map((text, i) => `${String(i + 1).padStart(4)}  ${text}`).join('\n'));
  $('code').scrollTop = Math.max(0, line - 4) * 22;
  notice('Reading the checked-out file from the shared workspace volume.');
}
async function choose() {
  selected = workspaces.find(w => w.id === $('workspace').value);
  $('files').replaceChildren(); $('results').replaceChildren(); $('results-title').hidden = true;
  show('No file selected', 'Choose a file to read its contents.');
  if (!selected) { $('metadata').textContent = 'Create a workspace to begin.'; notice(''); return; }
  $('metadata').textContent = `${selected.reference} → ${selected.revision || 'unresolved'} · ${selected.state}\n${selected.location}`;
  const data = await api(workspaceRoute('files'));
  for (const name of data.files) link($('files'), name, () => openFile(name));
  notice(`${data.files.length} files${data.truncated ? ' (listing limited)' : ''}. Select one or search its contents.`);
}
async function refresh(id = selected?.id) {
  workspaces = (await api('/workspaces')).workspaces || [];
  $('workspace').replaceChildren();
  for (const workspace of workspaces) {
    const option = document.createElement('option'); option.value = workspace.id;
    option.textContent = `${workspace.repository.path || 'Local demo'} · ${workspace.reference} · ${workspace.id.slice(0, 8)} · ${workspace.state}`;
    $('workspace').append(option);
  }
  if (workspaces.some(w => w.id === id)) $('workspace').value = id;
  if (!workspaces.length) $('workspace').append(new Option('No workspaces yet', ''));
  await choose();
}
$('source').addEventListener('change', controls);
$('workspace').addEventListener('change', () => run(choose));
$('refresh').addEventListener('click', () => run(() => refresh()));
$('create-form').addEventListener('submit', event => {
  event.preventDefault(); run(async () => {
    const workspace = await api('/workspaces', 'POST', { source: $('source').value, repository: $('repository').value, reference: $('reference').value });
    await refresh(workspace.id);
  });
});
$('search-form').addEventListener('submit', event => {
  event.preventDefault(); run(async () => {
    const data = await api(`${workspaceRoute('search')}?q=${encodeURIComponent($('query').value)}`);
    $('results').replaceChildren(); $('results-title').hidden = false;
    for (const match of data.matches) link($('results'), `${match.path}:${match.line} — ${match.text}`, () => openFile(match.path, match.line));
    notice(`${data.matches.length} matches${data.truncated ? ' (search limited)' : ''}; ${data.skipped} files skipped.`);
  });
});
$('retain').addEventListener('click', () => run(async () => { await api(workspaceRoute('retain'), 'POST', {}); await refresh(); notice('Retained. This checkout remains until you explicitly remove it.'); }));
$('remove').addEventListener('click', () => run(async () => { await api(workspaceRoute(), 'DELETE'); selected = undefined; await refresh(); notice('Workspace removed. Other checkouts are unchanged.'); }));
$('status').addEventListener('click', () => run(async () => {
  const [status, diff] = await Promise.all([api(workspaceRoute('status')), api(workspaceRoute('diff'))]);
  const decode = value => new TextDecoder().decode(Uint8Array.from(atob(value || ''), c => c.charCodeAt(0)));
  const files = (status.files || []).map(f => `${f.index}${f.worktree} ${f.path}`).join('\n');
  show('Workspace status & diff', `HEAD ${status.revision}\nBranch: ${status.branch || '(detached)'}\n\n${files || 'Working copy is clean.'}\n\nSTAGED\n${decode(diff.staged) || '(none)'}\n\nUNSTAGED\n${decode(diff.unstaged) || '(none)'}`);
  notice('Status and diffs come from forge-workspaces.');
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
