'use strict';
// Front end for MiniCRM. Everything on screen comes from the REST API under /api.

const $ = (s, el = document) => el.querySelector(s);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const money = n => '$' + Math.round(n || 0).toLocaleString('en-US');
const moneyShort = n => (n >= 1e6 ? '$' + (n / 1e6).toFixed(2) + 'M' : n >= 1e3 ? '$' + Math.round(n / 1e3) + 'K' : money(n));
const monthNames = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const fmtDate = iso => {
  if (!iso) return '—';
  const m = /^(\d{4})-(\d{1,2})-(\d{1,2})/.exec(iso);
  if (!m) return '—';
  return `${Number(m[2])} ${monthNames[Number(m[2]) - 1]}, ${m[1]}`;
};
const fmtStamp = iso => {
  if (!iso) return '';
  const d = new Date(iso);
  const h = d.getHours();
  const m = String(d.getMinutes()).padStart(2, '0');
  const ampm = h >= 12 ? 'PM' : 'AM';
  const h12 = h % 12 || 12;
  return `${d.getDate()} ${monthNames[d.getMonth()]}, ${d.getFullYear()}, ${h12}:${m} ${ampm}`;
};
const fmtDateTime = iso => {
  if (!iso) return '';
  const d = new Date(iso);
  const h = d.getHours();
  const m = String(d.getMinutes()).padStart(2, '0');
  const s = String(d.getSeconds()).padStart(2, '0');
  const ampm = h >= 12 ? 'PM' : 'AM';
  const h12 = h % 12 || 12;
  return `${d.getDate()} ${monthNames[d.getMonth()]}, ${d.getFullYear()}, ${h12}:${m}:${s} ${ampm}`;
};
const fmtDiff = (a, b) => {
  if (!a || !b) return '';
  const diffDays = Math.round(Math.abs(new Date(a) - new Date(b)) / (1000 * 60 * 60 * 24));
  if (diffDays < 1) return 'less than a day';
  if (diffDays < 14) return `${diffDays} day${diffDays !== 1 ? 's' : ''}`;
  const weeks = Math.round(diffDays / 7);
  return `${weeks} week${weeks !== 1 ? 's' : ''}`;
};

const VIEWS = {
  buckets: { title: 'Buckets', sub: 'Active accounts sorted into the priority waterfall. Pick a bucket to work it, or add your own.' },
  book: { title: 'Book of business', sub: 'Every account assigned to you, including inactive ones.' },
  noar: { title: 'No autorenew', sub: 'Active accounts with AutoRenew off and a renewal due in under 90 days, whatever their ARR.' },
  notes: { title: 'Notes log', sub: 'Every saved note in one place, newest first.' },
  activity: { title: 'Activity', sub: 'Every change made through the app or the API.' },
  api: { title: 'API', sub: 'Everything this app does goes through these endpoints. Use them from scripts or other tools.' },
};
const FILTERS = [
  { key: 'accountOwner', label: 'Account Owner', options: m => m.accountOwners },
  { key: 'renewalManager', label: 'Renewal manager', options: m => m.renewalManagers },
  { key: 'autorenew', label: 'AutoRenew', options: () => ['Yes', 'No'] },
  { key: 'fiscalQuarter', label: 'Fiscal quarter', options: m => m.fiscalQuarters },
  { key: 'risk', label: 'Risk flag', options: () => ['Yes', 'No', 'Unset'] },
];
const PAGE = 50;

// What Salesforce and sf reported for the last MTM comments save or refresh, per account.
function mtmStatusHtml(a) {
  const s = state.mtmStatus;
  return s && s.id === a.id ? `<pre class="sf-status${s.status === 'failed' ? ' bad' : ''}" id="mtm-status">${esc(s.lines.join('\n'))}</pre>` : '';
}
function showMtmStatus(a) {
  if (!a || !a.mtmSync) return;
  const m = a.mtmSync;
  state.mtmStatus = { id: a.id, status: m.status, lines: m.lines && m.lines.length ? m.lines : [m.message] };
  const wrap = $('#mtm-status-wrap');
  if (wrap && state.openId === a.id) wrap.innerHTML = mtmStatusHtml(a);
}

const state = {
  view: 'buckets', bucketId: null, bookStatus: 'active', q: '', filters: {},
  sort: null, order: null, offset: 0, notes: [], meta: null, summary: null, openId: null, tableSeq: 0, mtmStatus: null,
  selected: new Set(), pageIds: new Set(),
};

// ------------------------------------------------------------------ API client
async function api(path, opts = {}) {
  const headers = { ...(opts.body ? { 'Content-Type': 'application/json' } : {}) };
  let key = '';
  try { key = localStorage.getItem('csm-api-key') || ''; } catch (e) {}
  if (key) headers['MiniCRM-X-Key'] = key;
  const res = await fetch('/api' + path, { method: opts.method || 'GET', headers, body: opts.body ? JSON.stringify(opts.body) : undefined });
  if (res.status === 401) {
    const entered = window.prompt('This server requires an API key:');
    if (entered) { try { localStorage.setItem('csm-api-key', entered.trim()); } catch (e) {} return api(path, opts); }
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `Request failed (${res.status})`);
  return data;
}

let toastTimer;
function toast(msg, isError) {
  const el = $('#toast');
  el.textContent = msg;
  el.className = 'toast' + (isError ? ' error' : '');
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, isError ? 5000 : 1800);
}
const fail = e => toast(e.message, true);

// ------------------------------------------------------------------ small renderers
function daysPill(a) {
  if (a.days === null) return '<span class="pill">Evergreen</span>';
  if (a.days < 0) return `<span class="pill red">${-a.days}d overdue</span>`;
  const cls = a.days < 30 ? 'red' : a.days < 60 ? 'orange' : a.days <= 120 ? 'amber' : '';
  return `<span class="pill ${cls}">${a.days}d</span>`;
}
function statusPill(s) {
  if (!s) return '<span class="muted">—</span>';
  const cls = { Red: 'red', Yellow: 'amber', Purple: 'purple', Green: 'green' }[s] || (/churn|downgrade/i.test(s) ? 'red' : '');
  return `<span class="pill ${cls}">${esc(s)}</span>`;
}
const bucketOf = id => (state.summary ? state.summary.buckets.find(b => b.id === id) : null);
const bucketPill = a => `<span class="pill dot" style="--c:${esc((bucketOf(a.bucketId) || {}).color || '')}">${esc(a.bucketName)}</span>`;
const arCell = v => (v === true ? '<span class="pill green">Yes</span>' : v === false ? '<span class="pill red">No</span>' : '<span class="muted">—</span>');
const riskCell = v => (v === 'Yes' ? '<span class="pill red">At risk</span>' : v === 'No' ? '<span class="pill green">OK</span>' : '<span class="muted">—</span>');
function touch(label, date, ago, fresh, stale) {
  if (!date) return `<span class="muted">${label} —</span>`;
  const style = ago <= fresh ? 'color:var(--accent-text)' : ago > stale ? 'color:var(--red)' : '';
  return `<span style="${style}">${label} ${ago}d ago</span>`;
}
const engagementCell = a => `<div>${touch('Email', a.lastEmail, a.lastEmailDaysAgo, 14, 45)}</div><div class="sub">${touch('Gong', a.lastGong, a.lastGongDaysAgo, 30, 90)}</div>`;

// ------------------------------------------------------------------ shell
function setChrome() {
  const v = VIEWS[state.view];
  $('#view-title').textContent = v.title;
  $('#view-sub').textContent = v.sub;
  document.querySelectorAll('#nav a').forEach(a => a.classList.toggle('active', a.dataset.view === state.view));
  $('.search').style.visibility = ['buckets', 'book', 'noar'].includes(state.view) ? 'visible' : 'hidden';
  const s = state.summary, m = state.meta;
  if (m) { $('#app-version').textContent = `MiniCRM ${m.version === 'dev' ? 'dev' : 'v' + m.version}`; checkForUpdates(m.version); }
  if (s) { $('#nav-noar').textContent = s.noAutorenewUnder90.count || ''; $('#nav-notes').textContent = s.notesAdded || ''; }
}

async function refreshSummary() {
  [state.meta, state.summary] = await Promise.all([api('/meta'), api('/summary')]);
  if (!bucketOf(state.bucketId)) state.bucketId = state.summary.buckets[0].id; // the selected bucket may have been deleted
  setChrome();
}

function kpisHtml() {
  const s = state.summary;
  const k = (label, value, cls = '') => `<div class="kpi"><div class="kpi-label">${label}</div><div class="kpi-value ${cls}">${value}</div></div>`;
  return `<div class="kpis">
    ${k('Active accounts', s.active.toLocaleString())}
    ${k('Active ARR', moneyShort(s.activeArr))}
    ${k('Flagged at risk', s.flaggedAtRisk, s.flaggedAtRisk ? 'red' : '')}
    ${k('Cadence set', s.cadenceSet)}
    ${k('Accounts with notes', s.notesAdded)}
    ${k('Inactive', s.inactive)}
  </div>`;
}

function bucketsHtml() {
  const buckets = state.summary.buckets;
  const empty = buckets.every(b => b.system) ? `<div class="panel banner">
      <div><b>No buckets with conditions yet.</b> Until you add some, every account with a term end date sits in ${esc(buckets[0].name)}.</div>
      <div class="banner-actions"><button type="button" class="btn" id="add-defaults">Add the standard six</button>
        <button type="button" class="btn secondary" id="add-bucket">Add my own bucket</button></div></div>` : '';
  return `<div class="buckets-wrap">${empty}<div class="buckets">${buckets.map(b => `
    <button type="button" class="bucket ${b.id === state.bucketId ? 'active' : ''}" style="--c:${esc(b.color)}" data-bucket="${b.id}">
      <div class="bucket-name">${esc(b.name)}</div>
      <div class="bucket-count">${b.count}</div>
      <div class="bucket-arr">${moneyShort(b.arr)} ARR</div>
      <div class="bucket-cond">${esc(b.rule)}</div>
    </button>`).join('')}
    <button type="button" class="bucket add" id="add-bucket-card"><span>+</span> Add bucket</button></div></div>`;
}

function toolbarHtml() {
  const m = state.meta;
  const selects = FILTERS.filter(f => !(state.view === 'noar' && f.key === 'autorenew')).map(f => {
    const cur = state.filters[f.key] || '';
    return `<select data-filter="${f.key}" class="${cur ? 'on' : ''}" aria-label="${f.label}">
      <option value="">${f.label}: all</option>
      ${f.options(m).map(o => `<option value="${esc(o)}" ${o === cur ? 'selected' : ''}>${esc(o)}</option>`).join('')}
    </select>`;
  }).join('');
  const seg = state.view !== 'book' ? '' : `<div class="seg" id="book-seg">${['active', 'inactive', 'all'].map(s =>
    `<button type="button" data-status="${s}" class="${state.bookStatus === s ? 'active' : ''}">${s[0].toUpperCase() + s.slice(1)}</button>`).join('')}</div>`;
  const any = Object.values(state.filters).some(Boolean) || state.q;
  return `<div class="toolbar">${seg}${selects}${any ? '<button type="button" class="link-btn" id="clear-filters">Clear filters</button>' : ''}
    <span class="spacer"></span><span class="count" id="table-count"></span></div>`;
}

function bulkBarHtml() {
  if (!state.selected.size) return '';
  return `<div class="bulk-bar">
    <span class="bulk-count">${state.selected.size} selected</span>
    <button type="button" class="btn secondary bulk-action" data-bulk-risk="Yes">Risk: At risk</button>
    <button type="button" class="btn secondary bulk-action" data-bulk-risk="No">Risk: Not at risk</button>
    <button type="button" class="btn secondary bulk-action" data-bulk-cadence="Weekly">Cadence: Weekly</button>
    <button type="button" class="link-btn bulk-action" data-bulk-clear>Clear selection</button>
  </div>`;
}

// ------------------------------------------------------------------ account table
function columns() {
  const showBucket = state.view !== 'buckets';
  return [
    { key: null, select: true, label: 'Select', cell: a => `<input type="checkbox" data-row-id="${a.id}" ${state.selected.has(a.id) ? 'checked' : ''}>` },
    { key: 'name', label: 'Account', cell: a => `<div class="acct" title="${esc(a.name)}">${esc(a.name)}</div><div class="sub">${a.status === 'Inactive' ? esc(a.inactiveReason || 'Inactive') : esc(a.accountOwner || 'No owner')}</div>` },
    showBucket && { key: 'bucket', label: 'Bucket', cell: bucketPill },
    { key: 'arr', label: 'ARR', right: true, cell: a => `<span class="num">${money(a.arr)}</span>` },
    { key: 'days', label: 'Renewal', cell: a => `${daysPill(a)}<div class="sub">${a.termEnd ? fmtDate(a.termEnd) + ' · ' + esc(a.fiscalQuarter) : 'No term end'}</div>` },
    { key: 'autorenew', label: 'AutoRenew', cell: a => arCell(a.autorenew) },
    { key: 'renewalStatus', label: 'Status', cell: a => statusPill(a.renewalStatus) },
    { key: 'ews', label: 'EWS', right: true, cell: a => (a.ews != null ? `<span class="num">${esc(a.ews)}</span>` : '<span class="muted">—</span>') },
    { key: 'renewalManager', label: 'Renewal manager', cell: a => (a.renewalManager ? esc(a.renewalManager) : '<span class="muted">—</span>') },
    { key: 'engagement', label: 'Last touch', cell: engagementCell },
    { key: 'risk', label: 'Risk', cell: a => riskCell(a.risk) },
    { key: 'cadence', label: 'Cadence', cell: a => (a.cadence ? `<span class="pill blue">${esc(a.cadence)}</span>` : '<span class="muted">—</span>') },
    { key: null, label: 'Notes', cell: a => (a.noteCount ? `<div class="note-preview" title="${esc(a.notes[0].text)}">${esc(a.notes[0].text)}</div>${a.noteCount > 1 ? `<div class="sub">${a.noteCount} notes</div>` : ''}` : '<span class="muted">—</span>') },
  ].filter(Boolean);
}

function tableQuery() {
  const p = new URLSearchParams();
  if (state.view === 'buckets') { p.set('status', 'active'); p.set('bucketId', state.bucketId); }
  if (state.view === 'book') p.set('status', state.bookStatus);
  if (state.view === 'noar') { p.set('status', 'active'); p.set('autorenew', 'no'); p.set('maxDays', '89'); }
  for (const [k, v] of Object.entries(state.filters)) if (v) p.set(k, v.toLowerCase());
  if (state.q) p.set('q', state.q);
  const defaultSort = state.view === 'noar' || state.view === 'buckets' && (bucketOf(state.bucketId) || {}).autorenew === 'no' ? ['days', 'asc'] : ['arr', 'desc'];
  p.set('sort', state.sort || defaultSort[0]);
  p.set('order', state.order || defaultSort[1]);
  p.set('limit', PAGE);
  p.set('offset', state.offset);
  return p;
}

async function loadTable() {
  const host = $('#table-host');
  if (!host) return;
  const seq = ++state.tableSeq;
  const p = tableQuery();
  let data;
  try { data = await api('/accounts?' + p); } catch (e) { return fail(e); }
  if (seq !== state.tableSeq || !host.isConnected) return; // a newer request superseded this one
  const cols = columns();
  const sortKey = p.get('sort'), dir = p.get('order');
  $('#table-count').textContent = `${data.total.toLocaleString()} account${data.total === 1 ? '' : 's'} · ${money(data.totalArr)} ARR`;
  if (!data.total) {
    host.innerHTML = '<div class="empty">No accounts match these filters.</div>';
    return;
  }
  state.pageIds = new Set(data.items.map(a => a.id));
  const head = cols.map(c => {
    if (c.select) {
      const allSelected = data.items.length && data.items.every(a => state.selected.has(a.id));
      return `<th><input type="checkbox" data-select-all ${allSelected ? 'checked' : ''} aria-label="Select all on this page"></th>`;
    }
    const sorted = c.key === sortKey;
    const ariaDir = dir === 'desc' ? 'descending' : 'ascending';
    const ariaSort = sorted ? ` aria-sort="${ariaDir}"` : '';
    const arrow = sorted ? `<span class="arrow" aria-hidden="true">${dir === 'desc' ? '↓' : '↑'}</span>` : '';
    if (c.key) {
      return `<th scope="col" class="${c.right ? 'r' : ''}" data-sort="${c.key}"${ariaSort}>
        <button type="button">${c.label}${arrow}</button>
      </th>`;
    }
    return `<th class="${c.right ? 'r' : ''}">${c.label}</th>`;
  }).join('');
  const body = data.items.map(a => `<tr data-id="${a.id}" class="${a.id === state.openId ? 'selected' : ''}">${cols.map(c => `<td data-label="${esc(c.label)}" class="${c.select ? 'sel' : ''} ${c.right ? 'r' : ''}">${c.cell(a)}</td>`).join('')}</tr>`).join('');
  const from = data.offset + 1, to = data.offset + data.items.length;
  const captions = { buckets: 'Active accounts sorted into priority buckets', book: 'Book of business — every account including inactive ones', noar: 'No autorenew — active accounts renewing in under 90 days' };
  host.innerHTML = `<div class="table-wrap" role="region" aria-labelledby="table-caption-${state.view}" tabindex="0"><table><caption id="table-caption-${state.view}" class="visually-hidden">${captions[state.view] || 'Accounts'}</caption><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table></div>
    <div class="pager"><span>${from}–${to} of ${data.total.toLocaleString()}</span>
      <button type="button" id="prev" ${data.offset ? '' : 'disabled'}>Previous</button>
      <button type="button" id="next" ${to < data.total ? '' : 'disabled'}>Next</button></div>`;
}

// ------------------------------------------------------------------ views
function renderAccountsView() {
  const s = state.summary;
  const bucket = bucketOf(state.bucketId);
  const top = state.view === 'buckets' ? kpisHtml() + bucketsHtml() : state.view === 'book' ? kpisHtml() : '';
  const rule = state.view === 'buckets' ? `<div class="rule" style="--c:${esc(bucket.color)}">
      <div class="rule-head">
        <span class="rule-badge">${esc(bucket.name)}</span>
        <span class="chips">${bucket.conditions.map(c => `<span class="chip">${esc(c)}</span>`).join('')}</span>
        <button type="button" class="link-btn" data-edit-bucket="${bucket.id}">Edit bucket</button>
      </div>
      <div class="rule-desc">${esc(bucket.description)}</div>
    </div>` : '';
  $('#content').innerHTML = `${top}<div class="panel">${bulkBarHtml()}${toolbarHtml()}${rule}<div id="table-host"><div class="empty">Loading…</div></div></div>`;
  loadTable();
}

async function renderNotes() {
  const data = await api('/notes');
  if (!data.total) {
    $('#content').innerHTML = '<div class="panel"><div class="empty">No notes yet. Open any account and add one — it shows up here and stays with the account even when its bucket changes.</div></div>';
    return;
  }
  let lastDay = null;
  const rows = data.items.map(n => {
    const day = n.updatedAt ? new Date(n.updatedAt).toDateString() : 'Unknown date';
    const header = day !== lastDay ? `<div class="feed-day">${esc(day)}</div>` : '';
    lastDay = day;
    return `${header}<div class="feed-item" data-id="${n.accountId}">
      <div class="feed-title">${esc(n.name)} ${bucketPill(n)}</div>
      <div class="feed-time">${esc(n.accountOwner)}${n.accountOwner ? ' · ' : ''}${fmtStamp(n.updatedAt)}</div>
      <div class="feed-text">${esc(n.note)}</div></div>`;
  }).join('');
  $('#content').innerHTML = `<div class="panel feed">${rows}</div>`;
}

const FIELD_LABELS = { apiKey: 'API key', bucket: 'Bucket', risk: 'Risk flag', cadence: 'Cadence', notes: 'Notes', details: 'Details', mtmComments: 'MTM comments', mtmSync: 'Salesforce sync', renewalManager: 'Renewal manager', renewalManagerLink: 'Renewal manager link', contract: 'Contract update', arr: 'ARR', termEnd: 'Term end', autorenew: 'AutoRenew', accountOwner: 'Account Owner', accountLink: 'Account link', accountOwnerLink: 'Account Owner link', slackChannel: 'Slack channel', renewalStatus: 'Renewal status', ews: 'EWS', import: 'Import', asOfDate: 'Day-count date', created: 'Created', deleted: 'Deleted', name: 'Name', lastEmail: 'Last email', lastGong: 'Last Gong' };
async function renderActivity() {
  const data = await api('/activity?limit=200');
  if (!data.total) { $('#content').innerHTML = '<div class="panel"><div class="empty">Nothing has changed yet.</div></div>'; return; }
  const rows = data.items.map(e => `<div class="feed-item" ${e.accountId ? `data-id="${e.accountId}"` : ''}>
      <div class="feed-title">${esc(e.accountName || 'Workspace')} <span class="pill">${esc(FIELD_LABELS[e.field] || e.field)}</span></div>
      <div class="feed-time">${fmtDateTime(e.at)}</div>
      <div class="feed-text">${e.oldValue ? `<del>${esc(e.oldValue)}</del> → ` : ''}${esc(e.newValue ?? 'cleared')}</div></div>`).join('');
  $('#content').innerHTML = `<div class="panel feed">${rows}</div>`;
}

const localKey = () => { try { return localStorage.getItem('csm-api-key') || ''; } catch (e) { return ''; } };

// API keys: create (shown once), list and revoke. state.newKey is the key just created.
function keysPanelHtml(keys) {
  const k = state.newKey, o = location.origin, mine = localKey().slice(0, 10);
  const status = !keys.required
    ? 'No key is required right now, so any program on this computer can call the API. Creating the first key turns the requirement on for every call, including this page; this browser will remember the new key.'
    : keys.envKey ? 'A key is required on every API call. The server was also started with <code>MINICRM_APIKEY</code>, which always works.'
      : 'A key is required on every API call.';
  const created = k ? `<div class="key-new" role="status">
      <b>Copy this key now. It is not stored and will not be shown again.</b>
      <div class="key-row"><code id="key-value">${esc(k.key)}</code><button type="button" class="btn secondary" id="key-copy">Copy</button></div>
      <pre>export MINICRM_APIKEY="${esc(k.key)}"
curl -H "MiniCRM-X-Key: $MINICRM_APIKEY" ${o}/api/summary</pre></div>` : '';
  const list = keys.items.length ? keys.items.map(x => `<div class="key-item">
      <div><b>${esc(x.name)}</b>${x.prefix === mine ? ' <span class="pill green">used by this browser</span>' : ''}
        <div class="sub"><code>${esc(x.prefix)}…</code> · created ${fmtStamp(x.createdAt)}</div></div>
      <button type="button" class="link-btn danger" data-key-revoke="${x.id}" data-name="${esc(x.name)}" data-prefix="${esc(x.prefix)}">Revoke</button></div>`).join('')
    : '<small class="muted">No keys created yet.</small>';
  return `<div class="panel panel-pad" id="keys-panel">
      <h3>API keys</h3>
      <p>${status}</p>
      ${created}
      <div class="key-row"><input id="key-name" type="text" maxlength="100" placeholder="What is this key for? For example: my laptop scripts" aria-label="Name for the new key">
        <button type="button" class="btn" id="key-create">Create key</button></div>
      <div class="key-list">${list}</div>
      <small class="muted">Send a key in the <code>MiniCRM-X-Key</code> header. If every key is lost, run <code>./minicrm keys-reset</code> on the computer running the server.</small>
    </div>`;
}

async function renderApi() {
  const [data, keys] = await Promise.all([api(''), api('/keys')]);
  const o = location.origin;
  const s0 = state.summary.buckets[0].id;
  const rows = data.endpoints.map(e => `<div class="api-row"><span class="method ${e.method}">${e.method}</span><code>${esc(e.path)}</code><span>${esc(e.description)}</span></div>`).join('');
  $('#content').innerHTML = `<div class="stack">
    ${keysPanelHtml(keys)}
    <div class="panel api-list">${rows}</div>
    <div class="panel panel-pad"><h3>Using a key in the examples</h3>
      <p>Every example below sends the key from the <code>MINICRM_APIKEY</code> variable in your shell. Set it once per terminal. While no key is required, the header is ignored, so the examples work either way.</p>
      <pre>export MINICRM_APIKEY="paste-your-key-here"</pre></div>
    <div class="panel panel-pad"><h3>List accounts</h3>
      <p>Filters: <code>status</code>, <code>bucketId</code>, <code>accountOwner</code>, <code>renewalManager</code>, <code>autorenew</code>, <code>fiscalQuarter</code>, <code>risk</code>, <code>renewalStatus</code>, <code>minDays</code>/<code>maxDays</code>, <code>minArr</code>/<code>maxArr</code>, <code>hasNotes</code>, <code>q</code>. Plus <code>sort</code>, <code>order</code>, <code>limit</code>, <code>offset</code>.</p>
      <p>Buckets are referred to by their numeric <code>id</code>, which <code>GET /api/buckets</code> lists. Several ids can be comma-separated.</p>
      <pre>curl -H "MiniCRM-X-Key: $MINICRM_APIKEY" ${o}/api/buckets

curl -H "MiniCRM-X-Key: $MINICRM_APIKEY" "${o}/api/accounts?bucketId=${s0}&amp;sort=days&amp;limit=10"</pre></div>
    <div class="panel panel-pad"><h3>Flag an account, set its owner and add a note</h3>
      <pre>curl -X PATCH ${o}/api/accounts/1 \\
  -H "MiniCRM-X-Key: $MINICRM_APIKEY" \\
  -d '{"risk": "Yes", "cadence": "Weekly", "accountOwner": "Jane Doe", "notes": "Champion left; exec sync booked."}'</pre></div>
    <div class="panel panel-pad"><h3>Set links, Slack channel and details</h3>
      <p><code>accountLink</code> (Salesforce), <code>accountOwnerLink</code> and <code>renewalManagerLink</code> (Slack) must be empty, a web address starting with <code>http://</code> or <code>https://</code>, or <code>[text](https://…)</code>, of which the address is kept. <code>slackChannel</code> is text; write it as <code>[#channel](https://…)</code> to get a link. <code>details</code> is one text box, replaced each time; web addresses and <code>[text](https://…)</code> in it show as links. Send <code>null</code> to clear any of them. A <code>name</code> written as <code>[Name](https://…)</code> sets the name and <code>accountLink</code> together.</p>
      <pre>curl -X PATCH ${o}/api/accounts/1 \\
  -H "MiniCRM-X-Key: $MINICRM_APIKEY" \\
  -d '{"accountLink": "https://example.lightning.force.com/lightning/r/Account/001XX/view", "accountOwnerLink": "https://example.slack.com/team/U111", "renewalManager": "Sam Roe", "renewalManagerLink": "https://example.slack.com/team/U222", "slackChannel": "[#cust-acme](https://example.slack.com/archives/C123)", "details": "Exec sponsor: CTO."}'</pre></div>
    <div class="panel panel-pad"><h3>Fill an account from Salesforce</h3>
      <p>MiniCRM does not talk to Salesforce; this is the mapping to use when copying values over. <code>termEnd</code> and <code>autorenew</code> come from the account's open renewal opportunity: <code>Name</code> contains "Renewal" and <code>StageName</code> is <code>03 - Validation</code>.</p>
      <table class="map"><thead><tr><th>MiniCRM field</th><th>Salesforce object</th><th>Salesforce field</th></tr></thead><tbody>
        ${[['name', 'Account', 'Name'], ['accountLink', 'Account', 'https://example.lightning.force.com/lightning/r/<Account Id>/view'], ['ews', 'PD_Data__c (active subdomain)', 'subdomain_EWS__c'], ['renewalManager', 'Account', 'Renewal_Rep_Lookup__r.Name'], ['termEnd', 'Opportunity', 'Subscription_Term_End_Date__c'], ['lastEmail', 'Task (TaskSubtype = Email)', 'ActivityDate of the newest one'], ['lastGong', 'Gong__Gong_Call__c', 'Gong__Call_Start__c of the newest one'], ['autorenew', 'Opportunity', 'Subscription_Autorenew__c']]
    .map(([f, obj, sf]) => `<tr><td><code>${f}</code></td><td>${obj}</td><td><code>${esc(sf)}</code></td></tr>`).join('')}
      </tbody></table>
      <p><code>ews</code> is a number; the numeric text Salesforce returns, such as <code>"66"</code>, is accepted.</p>
      <pre>curl -X PATCH ${o}/api/accounts/1 \\
  -H "MiniCRM-X-Key: $MINICRM_APIKEY" \\
  -d '{"termEnd": "2027-05-22", "autorenew": true, "ews": "66", "renewalManager": "Sam Roe", "accountLink": "https://example.lightning.force.com/lightning/r/001XXXXXXXXXXXXXXX/view"}'</pre></div>
    <div class="panel panel-pad"><h3>Add and list notes</h3>
      <p>Each note is saved with the time it was added. An account's <code>notes</code> field lists all of its notes, newest first, and <code>noteCount</code> is how many it has.</p>
      <pre>curl -X POST ${o}/api/accounts/1/notes \\
  -H "MiniCRM-X-Key: $MINICRM_APIKEY" \\
  -d '{"text": "QBR moved to December."}'

curl -H "MiniCRM-X-Key: $MINICRM_APIKEY" ${o}/api/accounts/1/notes</pre></div>
    <div class="panel panel-pad"><h3>Create and change buckets</h3>
      <p>An account goes into the first bucket, in position order, whose conditions it meets. Every account is re-sorted after a change. Leave a limit out for "no limit".</p>
      <pre>curl -X POST ${o}/api/buckets \\
  -H "MiniCRM-X-Key: $MINICRM_APIKEY" \\
  -d '{"name": "Big and close", "color": "#c9372c", "autorenew": "no", "arrMin": 100000, "daysMin": 0, "daysMax": 45, "position": 1}'

curl -X PATCH ${o}/api/buckets/${s0} \\
  -H "MiniCRM-X-Key: $MINICRM_APIKEY" \\
  -d '{"name": "Renamed bucket"}'</pre></div>
    <div class="panel panel-pad"><h3>Record a renewal</h3>
      <p>The bucket is recomputed from the new values. Send <code>{"outcome": null}</code> to undo.</p>
      <pre>curl -X PUT ${o}/api/accounts/1/contract \\
  -H "MiniCRM-X-Key: $MINICRM_APIKEY" \\
  -d '{"outcome": "Renewed", "newArr": 42000, "newTermEnd": "2027-12-19", "newAutorenew": true}'</pre></div>
    <div class="panel panel-pad"><h3>Load a fresh export</h3>
      <p>Accounts are matched by name. Risk flags, cadence, notes and contract updates are kept. Accounts missing from the new list become inactive.</p>
      <pre>curl -X POST ${o}/api/import \\
  -H "MiniCRM-X-Key: $MINICRM_APIKEY" \\
  -d '{"snapshotDate": "2026-11-18", "accounts": [{"name": "Acme", "arr": 30000, "termEnd": "2027-03-01", "autorenew": false, "accountOwner": "Jane Doe"}]}'</pre></div>
  </div>`;
}

async function render() {
  setChrome();
  try {
    if (state.view === 'notes') await renderNotes();
    else if (state.view === 'activity') await renderActivity();
    else if (state.view === 'api') await renderApi();
    else renderAccountsView();
  } catch (e) { fail(e); }
}

// ------------------------------------------------------------------ drawer
// The computed, read-only part of the panel. Redrawn on its own after a field edit so
// the inputs below keep their focus and contents.
// Text as a link when a web address is set; plain text otherwise. Only http(s) is linked.
const linked = (text, url) => (/^https?:\/\//i.test(url || '') ? `<a class="ext" href="${esc(url)}" target="_blank" rel="noopener noreferrer">${esc(text)}</a>` : esc(text));
// Free text where [name](https://…) becomes a link. Escaped first, and only http(s) is linked.
// Bare web addresses become links too.
const mdLinks = s => esc(s).replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)|https?:\/\/[^\s<]*[^\s<.,;:!?)]/gi,
  (m, text, url) => `<a class="ext" href="${url || m}" target="_blank" rel="noopener noreferrer">${text || m}</a>`);
// Boxes that hold a name and its link together as [Name](https://…); the two are stored apart.
const LINK_FIELD = { name: 'accountLink' };
const withLink = (text, url) => (url ? `[${text}](${url})` : text);
const drawerTitleHtml = a => `<h2>${linked(a.name, a.accountLink)}</h2>
  <div class="tags">${bucketPill(a)}<span class="pill ${a.status === 'Active' ? 'green' : ''}">${a.status}</span>${a.renewalStatus ? statusPill(a.renewalStatus) : ''}</div>`;
const drawerSummaryHtml = a => (a.inactiveReason ? `<div class="notice">${esc(a.inactiveReason)}${a.inactiveSince ? ' · since ' + fmtDate(a.inactiveSince) : ''}</div>` : '');

const outcomeHint = o => {
  if (!o) return '';
  return {
    Renewed: 'Account stays Active, renewal status shows Green, and it moves to the bucket for the new values.',
    Churned: 'Account becomes Inactive.',
    'New CSM': 'Account becomes Inactive (reassigned to a new CSM).',
  }[o] || '';
};

// Calculated values shown as a hint under the field they derive from.
function fieldHints(a) {
  const renewed = a.contract.outcome === 'Renewed';
  const ago = n => (n === 0 ? 'Today' : n < 0 ? `In ${-n} days` : `${n} days ago`);
  return {
    arr: renewed && a.arr !== a.source.arr ? `Renewed at ${money(a.arr)}` : '',
    termEnd: a.termEnd
      ? `${renewed && a.termEnd !== a.source.termEnd ? `Renewed to ${fmtDate(a.termEnd)} · ` : ''}${a.days < 0 ? `${-a.days} days overdue` : `${a.days} days left`} · ${a.fiscalQuarter}`
      : 'No date means month-to-month',
    autorenew: renewed && a.autorenew !== a.source.autorenew ? `Renewed with AutoRenew ${a.autorenew ? 'on' : 'off'}` : '',
    renewalStatus: renewed && a.source.renewalStatus !== 'Green' ? 'Shown as Green while renewed' : '',
    lastEmail: a.lastEmail ? ago(a.lastEmailDaysAgo) : 'None logged',
    lastGong: a.lastGong ? ago(a.lastGongDaysAgo) : 'None logged',
  };
}
function applyHints(a) {
  for (const [field, text] of Object.entries(fieldHints(a))) {
    const el = $(`[data-hint="${field}"]`);
    if (el) el.textContent = text;
  }
}

// Account details as plain text; the Edit button swaps this for the inputs.
function detailsViewHtml(a) {
  const fact = (k, v, sub = '') => `<div><dt>${k}</dt><dd>${v}</dd>${sub ? `<small>${esc(sub)}</small>` : ''}</div>`;
  const h = fieldHints(a);
  const none = '<span class="muted">Not set</span>';
  return `<dl class="facts">
      ${fact('ARR', money(a.arr), h.arr && 'Renewed; export value ' + money(a.source.arr))}
      ${fact('Term end', a.termEnd ? fmtDate(a.termEnd) : 'Month-to-month', a.termEnd ? `${a.days < 0 ? `${-a.days} days overdue` : `${a.days} days left`} · ${a.fiscalQuarter}` : '')}
      ${fact('AutoRenew', a.autorenew === null ? none : a.autorenew ? 'Yes' : 'No')}
      ${fact('Renewal status', a.renewalStatus ? statusPill(a.renewalStatus) : none)}
      ${fact('Account Owner', a.accountOwner || a.accountOwnerLink ? linked(a.accountOwner || 'Open in Slack', a.accountOwnerLink) : none)}
      ${fact('Slack channel', a.slackChannel ? mdLinks(a.slackChannel) : none)}
      ${fact('Renewal manager', a.renewalManager || a.renewalManagerLink ? linked(a.renewalManager || 'Open in Slack', a.renewalManagerLink) : none)}
      ${fact('EWS', a.ews != null ? esc(a.ews) : none)}
      ${fact('Last email', a.lastEmail ? fmtDate(a.lastEmail) : 'None logged', a.lastEmail ? h.lastEmail : '')}
      ${fact('Last Gong', a.lastGong ? fmtDate(a.lastGong) : 'None logged', a.lastGong ? h.lastGong : '')}
    </dl>`;
}

// Every note on the open account, newest first, each with the time it was added.
const notesListHtml = notes => (notes.length
  ? notes.map(n => `<div class="note">
      <div class="note-head"><time>${fmtStamp(n.createdAt)}</time>
        <button type="button" class="link-btn" data-note-del="${n.id}" aria-label="Delete note from ${fmtStamp(n.createdAt)}">Delete</button></div>
      <div class="note-text">${esc(n.text)}</div></div>`).join('')
  : '<small class="muted">No notes yet. Each note you add is saved with its date and time.</small>');

function drawerHtml(a) {
  state.account = a;
  const c = a.contract, s = a.source;
  const seg = ['', 'Yes', 'No'].map(v => `<button type="button" data-risk="${v}" class="${(a.risk || '') === v ? 'active' : ''}">${v === '' ? 'Not set' : v === 'Yes' ? 'At risk' : 'Not at risk'}</button>`).join('');
  const renewed = c.outcome === 'Renewed';
  const hints = fieldHints(a);
  const hint = field => (field in hints ? `<small data-hint="${field}">${esc(hints[field])}</small>` : '');
  const input = (label, field, value, attrs = '') => `<label class="field"><span>${label}</span><input data-field="${field}" value="${esc(value ?? '')}" ${attrs}>${hint(field)}</label>`;
  return `<div class="drawer-head">
      <div id="drawer-title">${drawerTitleHtml(a)}</div>
      <button class="icon-btn" id="drawer-close" type="button" aria-label="Close" autofocus><svg viewBox="0 0 24 24"><path d="M6 6l12 12M18 6 6 18"/></svg></button>
    </div>
    <div class="drawer-body">
      <div id="drawer-summary" class="drawer-summary">${drawerSummaryHtml(a)}</div>
      <div>
        <div class="section-head">
          <h3 class="section-title">Account details</h3>
          <button type="button" class="link-btn" id="details-toggle">${state.editing ? 'Done' : 'Edit'}</button>
        </div>
        ${state.editing ? `
        ${input('Account name', 'name', withLink(a.name, a.accountLink), 'type="text" placeholder="Name, or [Name](https://…) to link it"')}
        <div class="row2">
          ${input('ARR ($)', 'arr', s.arr, 'type="text" inputmode="decimal"')}
          ${input('Term end', 'termEnd', s.termEnd, 'type="date"')}
        </div>
        <div class="row2">
          <label class="field"><span>AutoRenew</span><select data-field="autorenew">
            <option value="">Not set</option><option value="Yes" ${s.autorenew === true ? 'selected' : ''}>Yes</option><option value="No" ${s.autorenew === false ? 'selected' : ''}>No</option></select>${hint('autorenew')}</label>
          <label class="field"><span>Renewal status</span><select data-field="renewalStatus">
            <option value="">Not set</option>${[...new Set([...state.meta.renewalStatuses, s.renewalStatus].filter(Boolean))].map(o => `<option ${s.renewalStatus === o ? 'selected' : ''}>${esc(o)}</option>`).join('')}</select>${hint('renewalStatus')}</label>
        </div>
        <div class="row2">
          ${input('Account Owner', 'accountOwner', a.accountOwner, 'type="text" placeholder="Not set"')}
          ${input('Account Owner Slack link', 'accountOwnerLink', a.accountOwnerLink, 'type="url" placeholder="https://…"')}
        </div>
        <div class="row2">
          ${input('Renewal manager', 'renewalManager', a.renewalManager, 'type="text" placeholder="Add a name"')}
          ${input('Renewal manager Slack link', 'renewalManagerLink', a.renewalManagerLink, 'type="url" placeholder="https://…"')}
        </div>
        <div class="row2">
          ${input('Slack channel', 'slackChannel', a.slackChannel, 'type="text" placeholder="[#channel](https://…)"')}
          ${input('EWS', 'ews', a.ews, 'type="number" step="any" placeholder="Not set"')}
        </div>
        <div class="row2">
          ${input('Last email', 'lastEmail', a.lastEmail, 'type="date"')}
          ${input('Last Gong', 'lastGong', a.lastGong, 'type="date"')}
        </div>
        <small class="muted">${renewed ? 'A renewal is recorded below, so its values are what count for the bucket. ' : ''}Each field saves when you leave it. A new import overwrites these values.</small>` : `<div id="details-view">${detailsViewHtml(a)}</div>`}
      </div>
      <div>
        <h3 class="section-title">Tracking</h3>
        <div class="field"><span>Current risk</span><div class="seg" id="risk-seg">${seg}</div></div>
        <label class="field"><span>Cadence</span><select id="f-cadence"><option value="">Not set</option>${state.meta.cadenceOptions.map(o => `<option ${a.cadence === o ? 'selected' : ''}>${o}</option>`).join('')}</select></label>
        ${state.editing ? `<label class="field"><span>Details</span>
          <textarea data-field="details" placeholder="Standing information about this account… Links: https://… or [text](https://…)">${esc(a.details)}</textarea></label>`
    : `<div class="field"><span>Details</span><div class="note-text">${a.details ? mdLinks(a.details) : '<span class="muted">None yet. Click Edit to add.</span>'}</div></div>`}
      </div>
      ${a.termType === 'Evergreen' || a.mtmComments ? `<div>
        <label class="field"><span>MTM comments</span>
          <textarea data-field="mtmComments" placeholder="Why an ARR adjustment is expected on this month-to-month account, for which subdomain, with links to renewal opportunities.">${esc(a.mtmComments)}</textarea>
          <small class="muted">Start each new entry on its own line with the date, for example 11/24/26. When you leave the box it saves, and a new entry added at the top or at the end is added to Salesforce at the same end.</small></label>
        <div class="mtm-tools"><button type="button" class="link-btn" id="mtm-refresh">Refresh from Salesforce</button></div>
        <div id="mtm-status-wrap">${mtmStatusHtml(a)}</div>
      </div>` : ''}
      <div>
        <h3 class="section-title">Notes</h3>
        <div class="field">
          <textarea id="f-note" aria-label="New note" placeholder="Write a note…"></textarea>
          <div><button class="btn" id="note-add" type="button">Add note</button></div>
        </div>
        <div id="note-list" class="note-list">${notesListHtml(state.notes)}</div>
      </div>
      <div id="sync-info">
        ${(() => {
          const cu = a.aiUpdatedAt;
          const mu = a.manualUpdatedAt;
          const aiOlder = cu && mu && new Date(cu) < new Date(mu);
          const manualOlder = mu && cu && new Date(mu) < new Date(cu);
          let comparison = '';
          if (cu && mu) {
            const ahead = new Date(cu) > new Date(mu) ? 'AI' : 'Human';
            const behind = new Date(cu) > new Date(mu) ? 'human' : 'AI';
            comparison = `${ahead} ahead of ${behind} by ${fmtDiff(cu, mu)}`;
          }
          return `
          <div class="field"><span>Last synced by AI</span><span style="${aiOlder ? 'color: var(--red)' : ''}">${cu ? fmtDateTime(cu) : '<span class="muted">never</span>'}</span></div>
          ${mu ? `<div class="field"><span>Last manual renewal-field change</span><span style="${manualOlder ? 'color: var(--red)' : ''}">${fmtDateTime(mu)}</span></div>` : ''}
          ${comparison ? `<div class="field comparison">${comparison}</div>` : ''}
          `;
        })()}
      </div>
      <div>
        <h3 class="section-title">Update Contract Info</h3>
        <p class="muted">Record a renewal decision. The account stays in AI's sync flow — this UI is a manual fallback. Changes here count as a manual update and update "Last manual renewal-field change".</p>
        <div class="box">
          <label class="field"><span>Outcome</span><select id="f-outcome">
            <option value="">No change</option>${state.meta.contractOutcomes.map(o => `<option ${c.outcome === o ? 'selected' : ''}>${o}</option>`).join('')}</select>
            <small class="muted">${outcomeHint(c.outcome)}</small></label>
          <div id="renew-fields" ${renewed ? '' : 'hidden'}>
            <div class="row2">
              <label class="field"><span>New ARR ($)</span><input id="f-newarr" type="text" inputmode="decimal" value="${c.newArr ?? ''}" placeholder="${Math.round(a.source.arr)}"></label>
              <label class="field"><span>New term end</span><input id="f-newterm" type="date" value="${c.newTermEnd || ''}">${s.termEnd ? `<small class="muted">Current: ${fmtDate(s.termEnd)}</small>` : ''}</label>
            </div>
            <label class="field"><span>New AutoRenew</span><select id="f-newar">
              <option value="">Keep current</option><option value="Yes" ${c.newAutorenew === true ? 'selected' : ''}>Yes</option><option value="No" ${c.newAutorenew === false ? 'selected' : ''}>No</option></select>${s.autorenew === null ? '' : `<small class="muted">Current: ${s.autorenew ? 'Yes' : 'No'}</small>`}</label>
          </div>
          <button class="btn" id="contract-apply" type="button">Apply</button>
          ${c.updatedAt ? `<small class="muted" style="margin-left:8px">Last change on ${fmtDateTime(c.updatedAt)}</small>` : ''}
        </div>
      </div>
    </div>`;
}

async function openDrawer(id) {
  try {
    const a = await api('/accounts/' + id);
    state.notes = a.notes;
    state.openId = a.id;
    state.editing = false;
    const d = $('#drawer');
    d.innerHTML = drawerHtml(a);
    d.showModal();
    document.querySelectorAll('tbody tr').forEach(tr => tr.classList.toggle('selected', Number(tr.dataset.id) === a.id));
  } catch (e) { fail(e); }
}
function closeDrawer() {
  state.openId = null;
  $('#drawer').close();
  document.querySelectorAll('tbody tr.selected').forEach(tr => tr.classList.remove('selected'));
}

// Full redraw of the account panel that keeps a half-written note.
function redrawPanel(a) {
  const draft = $('#f-note') ? $('#f-note').value : '';
  $('#drawer').innerHTML = drawerHtml(a);
  if (draft) $('#f-note').value = draft;
}

/** Save a change for the open account, then refresh everything that could show it. */
async function save(request, message, redrawDrawer) {
  const id = state.openId;
  try {
    const a = await request(id);
    toast(message);
    await refreshSummary();
    if (state.openId === id) {
      state.account = a;
      if (redrawDrawer || state.editing) redrawPanel(a);
      else { state.account = a; $('#drawer-title').innerHTML = drawerTitleHtml(a); $('#drawer-summary').innerHTML = drawerSummaryHtml(a); applyHints(a); const dv = $('#details-view'); if (dv) dv.innerHTML = detailsViewHtml(a); }
    }
    if (['buckets', 'book', 'noar'].includes(state.view)) {
      // Keep the page skeleton (and scroll position); just refresh numbers and rows.
      const kp = $('.kpis'); if (kp) kp.outerHTML = kpisHtml();
      const bk = $('.buckets-wrap'); if (bk) bk.outerHTML = bucketsHtml();
      loadTable();
    } else render();
    return a;
  } catch (e) { fail(e); }
}
const patch = body => id => api('/accounts/' + id, { method: 'PATCH', body });

// ------------------------------------------------------------------ events
$('#drawer').addEventListener('click', e => {
  if (e.target === e.currentTarget) return closeDrawer(); // backdrop dismiss
  if (e.target.closest('#drawer-close, #bucket-cancel')) return closeDrawer();
  const bdel = e.target.closest('#bucket-delete');
  if (bdel) {
    if (!window.confirm(`Delete the bucket "${bdel.dataset.name}"? Its accounts move to the next bucket they match. This cannot be undone.`)) return;
    return api('/buckets/' + bdel.dataset.id, { method: 'DELETE' }).then(() => afterBucketsChange('Bucket deleted. Accounts re-sorted.')).catch(fail);
  }
  if (e.target.closest('#mtm-refresh')) {
    if (!window.confirm('Replace the MTM comments here with the current text from Salesforce? Text that is not in Salesforce yet is replaced too; the old text stays in the Activity log.')) return;
    const btn = $('#mtm-refresh');
    btn.disabled = true; btn.textContent = 'Refreshing…';
    return save(async id => {
      const a = await api(`/accounts/${id}/mtm-refresh`, { method: 'POST' });
      showMtmStatus(a);
      return a;
    }, 'MTM comments refreshed from Salesforce', true).then(a => { if (!a) { btn.disabled = false; btn.textContent = 'Refresh from Salesforce'; } });
  }
  if (e.target.closest('#details-toggle')) {
    state.editing = !state.editing;
    const body = $('.drawer-body'), top = body.scrollTop;
    redrawPanel(state.account);
    $('.drawer-body').scrollTop = top;
    if (state.editing) $('[data-field="name"]').focus();
    return;
  }
  const risk = e.target.closest('[data-risk]');
  if (risk) return save(patch({ risk: risk.dataset.risk || null }), 'Risk flag saved', true);
  if (e.target.closest('#note-add')) {
    const box = $('#f-note'), text = box.value.trim();
    if (!text) { toast('Write the note first', true); return box.focus(); }
    return save(async id => {
      const a = await api(`/accounts/${id}/notes`, { method: 'POST', body: { text } });
      state.notes = a.notes;
      return a;
    }, 'Note added').then(a => { if (a && $('#note-list')) { $('#note-list').innerHTML = notesListHtml(state.notes); $('#f-note').value = ''; } });
  }
  const del = e.target.closest('[data-note-del]');
  if (del) {
    if (!window.confirm('Delete this note? This cannot be undone.')) return;
    return save(async id => {
      const a = await api(`/accounts/${id}/notes/${del.dataset.noteDel}`, { method: 'DELETE' });
      state.notes = a.notes;
      return a;
    }, 'Note deleted').then(a => { if (a && $('#note-list')) $('#note-list').innerHTML = notesListHtml(state.notes); });
  }
  if (e.target.closest('#contract-apply')) {
    const outcome = $('#f-outcome').value || null;
    const body = { outcome };
    if (outcome === 'Renewed') Object.assign(body, { newArr: $('#f-newarr').value.trim() || null, newTermEnd: $('#f-newterm').value || null, newAutorenew: $('#f-newar').value || null });
    save(id => api(`/accounts/${id}/contract`, { method: 'PUT', body }), outcome ? `Marked ${outcome}` : 'Contract update cleared', true);
  }
});
// ------------------------------------------------------------------ bucket editor
// `b` is an existing bucket, or null for a new one.
function bucketEditorHtml(b) {
  const isNew = !b;
  b = b || { name: '', description: '', color: '#3c6fd6', autorenew: 'any', system: false };
  const ruleCount = state.summary.buckets.filter(x => !x.system).length;
  const num = (label, name, value, hint) => `<label class="field"><span>${label}</span><input name="${name}" type="text" inputmode="decimal" value="${value ?? ''}" placeholder="No limit"><small>${hint}</small></label>`;
  const ar = v => `<option value="${v}" ${b.autorenew === v ? 'selected' : ''}>${{ any: 'Any', no: 'No', yes: 'Yes' }[v]}</option>`;
  const positions = Array.from({ length: ruleCount + (isNew ? 1 : 0) }, (_, i) => i + 1);
  const conditions = b.system ? `<div class="notice">Built-in bucket. It has no conditions of its own: it takes ${b.kind === 'noterm' ? 'every account with no term end date' : 'the accounts no other bucket claims'}. You can rename and recolor it, but not delete it.</div>` : `
      <div>
        <h3 class="section-title">Conditions</h3>
        <label class="field"><span>AutoRenew</span><select name="autorenew">${['any', 'no', 'yes'].map(ar).join('')}</select></label>
        <div class="row2">
          ${num('ARR more than ($)', 'arrMin', b.arrMin, 'Above this amount')}
          ${num('ARR up to ($)', 'arrMax', b.arrMax, 'This amount or less')}
        </div>
        <div class="row2">
          ${num('Days to renewal, from', 'daysMin', b.daysMin, 'This many days or more')}
          ${num('Days to renewal, to', 'daysMax', b.daysMax, 'This many days or fewer')}
        </div>
        <label class="field"><span>Order</span><select name="position">${positions.map(p => `<option value="${p}" ${p === (isNew ? positions.length : b.position) ? 'selected' : ''}>${p}${p === 1 ? ' (checked first)' : ''}</option>`).join('')}</select>
          <small>An account must meet every condition, and goes into the first bucket in this order that it matches.</small></label>
      </div>`;
  return `<div class="drawer-head">
      <div><h2>${isNew ? 'New bucket' : esc(b.name)}</h2>
        ${isNew ? '' : `<div class="tags">${b.conditions.map(c => `<span class="chip">${esc(c)}</span>`).join('')}</div>`}</div>
      <button class="icon-btn" id="drawer-close" type="button" aria-label="Close"><svg viewBox="0 0 24 24"><path d="M6 6l12 12M18 6 6 18"/></svg></button>
    </div>
    <form class="drawer-body" id="bucket-form" data-id="${isNew ? '' : b.id}" novalidate>
      <div>
        <label class="field"><span>Name</span><input name="name" type="text" value="${esc(b.name)}" maxlength="100" autocomplete="off"></label>
        <label class="field"><span>Description</span><textarea name="description" class="short" maxlength="500">${esc(b.description)}</textarea></label>
        <label class="field"><span>Color</span><input name="color" type="color" value="${esc(b.color)}"></label>
      </div>
      ${conditions}
      <div><button class="btn" type="submit">${isNew ? 'Create bucket' : 'Save bucket'}</button> <button class="btn secondary" type="button" id="bucket-cancel">Cancel</button></div>
      ${isNew || b.system ? '' : `<div><button type="button" class="link-btn danger" id="bucket-delete" data-id="${b.id}" data-name="${esc(b.name)}">Delete this bucket</button></div>`}
    </form>`;
}

function openBucketEditor(id) {
  closeDrawer();
  const d = $('#drawer');
  d.innerHTML = bucketEditorHtml(id ? bucketOf(id) : null);
  d.showModal();
  if (!id) d.querySelector('[name="name"]').focus();
}

async function afterBucketsChange(message) {
  await refreshSummary();
  closeDrawer();
  state.offset = 0;
  await render();
  toast(message);
}

$('#drawer').addEventListener('submit', async e => {
  if (e.target.id !== 'bucket-form') return;
  e.preventDefault();
  const f = new FormData(e.target), id = e.target.dataset.id;
  const val = k => (String(f.get(k)).trim() === '' ? null : String(f.get(k)).trim());
  const body = { name: val('name') || '', description: val('description') || '', color: f.get('color') };
  if (f.has('autorenew')) {
    const days = k => (val(k) === null ? null : Number(val(k)));
    Object.assign(body, { autorenew: f.get('autorenew'), arrMin: val('arrMin'), arrMax: val('arrMax'), daysMin: days('daysMin'), daysMax: days('daysMax'), position: Number(f.get('position')) });
    if ([body.daysMin, body.daysMax].some(d => d !== null && !Number.isInteger(d))) return toast('Days must be whole numbers', true);
  }
  if (!body.name) { toast('Give the bucket a name', true); return e.target.querySelector('[name="name"]').focus(); }
  try {
    const saved = await api(id ? '/buckets/' + id : '/buckets', { method: id ? 'PATCH' : 'POST', body });
    state.bucketId = saved.id;
    await afterBucketsChange(id ? 'Bucket saved. Accounts re-sorted.' : 'Bucket created. Accounts re-sorted.');
  } catch (err) { fail(err); }
});

$('#drawer').addEventListener('change', e => {
  const t = e.target;
  if (t.dataset.field) {
    // Empty means "clear" for everything except name and ARR, which the server requires.
    const required = t.dataset.field === 'name' || t.dataset.field === 'arr';
    const label = t.closest('.field').querySelector('span').textContent.replace(' ($)', '');
    let body = { [t.dataset.field]: t.value.trim() === '' && !required ? null : t.value };
    const linkField = LINK_FIELD[t.dataset.field];
    if (linkField) {
      const m = t.value.trim().match(/^\[(.+)\]\((https?:\/\/\S+)\)$/i);
      body = m ? { [t.dataset.field]: m[1], [linkField]: m[2] } : { ...body, [linkField]: null };
    }
    save(patch(body), `${label} saved`).then(a => { if (a && a.mtmSync) { showMtmStatus(a); toast(a.mtmSync.message, a.mtmSync.status === 'failed'); } });
  } else if (t.id === 'f-cadence') save(patch({ cadence: t.value || null }), 'Cadence saved');
  else if (t.id === 'f-outcome') {
    $('#renew-fields').hidden = t.value !== 'Renewed';
    if (t.value === 'Renewed') $('#f-newarr').focus();
  }
});
// Slack's "Copy link" puts a rich link on the clipboard; a text box would keep only its text.
// Paste it into the Slack channel and name boxes as [text](address) instead (address only
// in a link-only box).
$('#drawer').addEventListener('paste', e => {
  const field = e.target.dataset.field;
  const linkOnly = ['accountOwnerLink', 'renewalManagerLink'].includes(field);
  if (field !== 'slackChannel' && field !== 'details' && !linkOnly && !LINK_FIELD[field]) return;
  const link = new DOMParser().parseFromString(e.clipboardData.getData('text/html'), 'text/html').querySelector('a[href^="http"]');
  if (!link) return;
  e.preventDefault();
  // execCommand counts as typing, so the box still saves when you leave it.
  document.execCommand('insertText', false, linkOnly ? link.href : `[${link.textContent.trim() || link.href}](${link.href})`);
});

$('#content').addEventListener('click', async e => {
  if (e.target.closest('#key-create')) {
    const name = $('#key-name').value.trim();
    if (!name) { toast('Name the key first', true); return $('#key-name').focus(); }
    return api('/keys').then(async before => {
      state.newKey = await api('/keys', { method: 'POST', body: { name } });
      // Keep this page working: adopt the key if none was needed until now, or none is saved here.
      if (!before.required || !localKey()) { try { localStorage.setItem('csm-api-key', state.newKey.key); } catch (err) {} }
      await render();
      toast('Key created. Copy it now.');
    }).catch(fail);
  }
  if (e.target.closest('#key-copy')) {
    return navigator.clipboard.writeText($('#key-value').textContent).then(() => toast('Key copied'), () => toast('Could not copy. Select the key and copy it by hand.', true));
  }
  const kr = e.target.closest('[data-key-revoke]');
  if (kr) {
    if (!window.confirm(`Revoke the key "${kr.dataset.name}"? Anything using it stops working at once. This cannot be undone.`)) return;
    return api('/keys/' + kr.dataset.keyRevoke, { method: 'DELETE' }).then(async () => {
      if (kr.dataset.prefix === localKey().slice(0, 10)) { try { localStorage.removeItem('csm-api-key'); } catch (err) {} }
      state.newKey = null;
      await render();
      toast('Key revoked');
    }).catch(fail);
  }
  const eb = e.target.closest('[data-edit-bucket]');
  if (eb) return openBucketEditor(Number(eb.dataset.editBucket));
  if (e.target.closest('#add-bucket, #add-bucket-card')) return openBucketEditor(null);
  if (e.target.closest('#add-defaults')) {
    return api('/buckets/defaults', { method: 'POST' }).then(() => afterBucketsChange('Standard buckets added. Accounts sorted.')).catch(fail);
  }
  const b = e.target.closest('[data-bucket]');
  if (b) { state.bucketId = Number(b.dataset.bucket); state.offset = 0; state.sort = state.order = null; return renderAccountsView(); }
  const st = e.target.closest('[data-status]');
  if (st) { state.bookStatus = st.dataset.status; state.offset = 0; return renderAccountsView(); }
  if (e.target.closest('#clear-filters')) { state.filters = {}; state.q = ''; $('#search').value = ''; state.offset = 0; return renderAccountsView(); }
  const th = e.target.closest('th[data-sort]');
  if (th) {
    const cur = tableQuery();
    state.order = cur.get('sort') === th.dataset.sort && cur.get('order') === 'asc' ? 'desc' : 'asc';
    state.sort = th.dataset.sort;
    state.offset = 0;
    return loadTable();
  }
  if (e.target.closest('#prev')) { state.offset = Math.max(0, state.offset - PAGE); return loadTable(); }
  if (e.target.closest('#next')) { state.offset += PAGE; return loadTable(); }
  const ba = e.target.closest('[data-bulk-risk], [data-bulk-cadence]');
  if (ba) {
    const body = {};
    if (ba.dataset.bulkRisk) body.risk = ba.dataset.bulkRisk;
    if (ba.dataset.bulkCadence) body.cadence = ba.dataset.bulkCadence;
    const ids = [...state.selected];
    try {
      await Promise.all(ids.map(id => api('/accounts/' + id, { method: 'PATCH', body })));
      await refreshSummary();
      state.selected.clear();
      loadTable();
      toast(`Updated ${ids.length} account${ids.length === 1 ? '' : 's'}`);
    } catch (e) { fail(e); }
    return;
  }
  if (e.target.closest('[data-bulk-clear]')) { state.selected.clear(); loadTable(); return; }
  if (e.target.closest('input, button, select, textarea')) return; // interactive cells handled elsewhere
  const row = e.target.closest('[data-id]');
  if (row) openDrawer(row.dataset.id);
});
$('#content').addEventListener('change', e => {
  const row = e.target.closest('[data-row-id]');
  if (row) {
    const id = Number(row.dataset.rowId);
    if (row.checked) state.selected.add(id); else state.selected.delete(id);
    loadTable();
    return;
  }
  const sa = e.target.closest('[data-select-all]');
  if (sa) {
    if (sa.checked) state.pageIds.forEach(id => state.selected.add(id));
    else state.pageIds.forEach(id => state.selected.delete(id));
    loadTable();
    return;
  }
  const f = e.target.dataset.filter;
  if (!f) return;
  state.filters[f] = e.target.value;
  state.offset = 0;
  renderAccountsView();
});


let searchTimer;
$('#search').addEventListener('input', e => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => {
    state.q = e.target.value.trim();
    state.offset = 0;
    const hadClear = !!$('#clear-filters');
    if (hadClear === !!(state.q || Object.values(state.filters).some(Boolean))) loadTable(); else renderAccountsView();
  }, 200);
});

// Update check: compares the running build with the latest GitHub release, once per page load.
// "dev" builds (plain `go build`, no version passed in) are never checked.
const UPDATE_REPO = 'jplassnibatt/minicrm';
// Is version a greater than b? For tags like 1.0.0, 1.0.1, 1.1.0.
function versionGt(a, b) {
  const pa = a.split('.').map(Number), pb = b.split('.').map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] || 0, y = pb[i] || 0;
    if (x !== y) return x > y;
  }
  return false;
}
let updateChecked = false;
async function checkForUpdates(current) {
  if (updateChecked || current === 'dev') return;
  updateChecked = true;
  const btn = $('#update-btn');
  try {
    const res = await fetch(`https://api.github.com/repos/${UPDATE_REPO}/releases/latest`, { headers: { Accept: 'application/vnd.github+json' } });
    if (!res.ok) return;
    const data = await res.json();
    const latest = (data.tag_name || data.name || '').replace(/^v/, '');
    if (!latest) return;
    const newer = versionGt(latest, current);
    btn.classList.toggle('update-available', newer);
    btn.dataset.state = newer ? 'update' : 'current';
    btn.textContent = newer ? `Update available (v${latest})` : versionGt(current, latest) ? `Ahead of latest version (v${latest})` : 'Up to date';
    btn.title = `You are running v${current}, latest is v${latest}`;
  } catch (e) { /* offline, rate-limited or a private repo: stay quiet */ }
}
// Opens the release page, except when the check found nothing newer to download.
$('#update-btn').addEventListener('click', e => {
  if (e.currentTarget.dataset.state !== 'current') window.open(`https://github.com/${UPDATE_REPO}/releases/latest`, '_blank', 'noopener');
});

// Export: one JSON file with every account (notes and contract updates included) and the buckets.
$('#export-btn').addEventListener('click', async () => {
  try {
    const data = await api('/export');
    const link = document.createElement('a');
    link.href = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }));
    link.download = `minicrm-export-${data.exportedAt.slice(0, 10)}.json`;
    link.click();
    URL.revokeObjectURL(link.href);
    toast(`Exported ${data.accounts.length} accounts and ${data.buckets.length} buckets`);
  } catch (err) { fail(err); }
});

// Import: a file made by Export (or any { "accounts": [...] } JSON). Accounts are matched by
// name; nothing is deleted and accounts missing from the file are left as they are.
$('#import-btn').addEventListener('click', () => $('#import-file').click());
$('#import-file').addEventListener('change', async e => {
  const file = e.target.files[0];
  e.target.value = ''; // so picking the same file again fires a change
  if (!file) return;
  try {
    let data;
    try { data = JSON.parse(await file.text()); } catch (err) { throw new Error(`${file.name} is not a JSON file`); }
    if (Array.isArray(data)) data = { accounts: data };
    if (!data || !Array.isArray(data.accounts) || !data.accounts.length) throw new Error(`${file.name} has no accounts in it`);
    const buckets = Array.isArray(data.buckets) ? data.buckets.length : 0;
    const n = (count, word) => `${count} ${word}${count === 1 ? '' : 's'}`;
    if (!window.confirm(`Import ${n(data.accounts.length, 'account')}${buckets ? ` and ${n(buckets, 'bucket')}` : ''} from ${file.name}?\n\nAccounts with the same name are updated with the file's values, new ones are added, and notes are added without duplicates. Nothing is deleted.`)) return;
    const res = await api('/import', { method: 'POST', body: { ...data, markMissingInactive: false } });
    await afterBucketsChange(`Imported: ${res.created} added, ${res.updated} updated${res.bucketsCreated ? `, ${n(res.bucketsCreated, 'bucket')} added` : ''}`);
  } catch (err) { fail(err); }
});

$('#theme-btn').addEventListener('click', () => {
  const root = document.documentElement;
  const dark = root.dataset.theme ? root.dataset.theme === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches;
  root.dataset.theme = dark ? 'light' : 'dark';
  try { localStorage.setItem('csm-theme', root.dataset.theme); } catch (e) {}
});

function route() {
  const v = location.hash.replace(/^#\/?/, '');
  state.view = VIEWS[v] ? v : 'buckets';
  state.newKey = null; // a created key is shown once, until you leave the page
  state.offset = 0;
  state.sort = state.order = null;
  closeDrawer();
  render();
}
window.addEventListener('hashchange', route);

refreshSummary().then(route).catch(fail);
