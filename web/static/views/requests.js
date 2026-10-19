import { api } from '../api.js';
import { h, fmtClock, fmtSize, debounce, field } from '../dom.js';
import { fail } from '../ui.js';
import { OPS } from '../ops.js';

const MAX_ROWS = 1000;

const statusClass = (s) => `s${Math.floor((s || 0) / 100)}`;

export function mount(root) {
  let es = null;
  let ac = null;
  let paused = false;
  let pending = [];
  let maxId = 0;
  let rows = 0;
  let seq = 0;

  const f = {
    bucket: h('input', { id: 'rq-bucket', type: 'text', placeholder: 'any', autocomplete: 'off' }),
    op: h('input', { id: 'rq-op', type: 'text', placeholder: 'any', list: 'rq-ops', autocomplete: 'off' }),
    key: h('input', { id: 'rq-key', type: 'text', placeholder: 'key prefix', autocomplete: 'off' }),
    errors: h('input', { id: 'rq-err', type: 'checkbox' }),
  };
  const pauseBtn = h('button', { class: 'btn sm', type: 'button', onclick: togglePause }, 'Pause');
  const clearBtn = h('button', { class: 'btn sm', type: 'button', onclick: onClear }, 'Clear');
  const live = h('span', { class: 'chip' }, 'connecting');
  const count = h('span', { class: 'muted' }, '0 rows');
  const empty = h('div', { class: 'state empty' }, 'No requests yet. Point an S3 client at this server.');
  const thead = h('thead', {}, h('tr', {}, ['Time', 'Method', 'Operation', 'Bucket / key', 'Status', 'Error', 'ms', 'In', 'Out', 'Fault']
    .map((t, i) => h('th', { class: i >= 6 && i <= 8 ? 'num' : '' }, t))));
  const table = h('table', { class: 'reqs' }, thead);

  const filters = () => ({ bucket: f.bucket.value.trim(), op: f.op.value.trim(), key: f.key.value, errors: f.errors.checked ? 'true' : '' });

  function entryRow(e) {
    const detail = h('tr', { class: 'detail', hidden: true }, h('td', { colspan: 10 }, h('pre', { class: 'json' }, JSON.stringify(e, null, 2))));
    const target = e.bucket ? `${e.bucket}${e.key ? `/${e.key}` : ''}` : e.path;
    const tr = h('tr', { class: 'click', tabindex: 0, role: 'button', 'aria-expanded': 'false' },
      h('td', { class: 'mono' }, fmtClock(e.time)),
      h('td', { class: 'mono' }, e.method),
      h('td', {}, e.op),
      h('td', { class: 'mono trunc', title: target }, target),
      h('td', {}, h('span', { class: `badge ${statusClass(e.status)}` }, e.status)),
      h('td', { class: 'mono' }, e.errorCode),
      h('td', { class: 'num' }, typeof e.durationMs === 'number' ? e.durationMs.toFixed(1) : ''),
      h('td', { class: 'num' }, e.bytesIn ? fmtSize(e.bytesIn) : ''),
      h('td', { class: 'num' }, e.bytesOut ? fmtSize(e.bytesOut) : ''),
      h('td', {}, e.faultId && h('span', { class: 'badge fault', title: e.faultId }, e.faultId)));
    const toggle = () => {
      detail.hidden = !detail.hidden;
      tr.setAttribute('aria-expanded', String(!detail.hidden));
    };
    tr.addEventListener('click', toggle);
    tr.addEventListener('keydown', (ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); toggle(); } });
    return h('tbody', {}, tr, detail);
  }

  function updateCount() {
    count.textContent = `${rows} row${rows === 1 ? '' : 's'}${pending.length ? `, ${pending.length} buffered` : ''}`;
    empty.hidden = rows > 0;
    pauseBtn.textContent = paused ? `Resume${pending.length ? ` (${pending.length})` : ''}` : 'Pause';
  }

  function add(entries) {
    for (const e of entries) {
      if (e.id <= maxId) continue;
      maxId = e.id;
      thead.after(entryRow(e));
      rows++;
      if (rows > MAX_ROWS) {
        table.lastElementChild.remove();
        rows--;
      }
    }
    updateCount();
  }

  function incoming(e) {
    if (paused) {
      pending.push(e);
      if (pending.length > MAX_ROWS) pending.shift();
      updateCount();
    } else {
      add([e]);
    }
  }

  function togglePause() {
    paused = !paused;
    if (!paused) {
      const p = pending;
      pending = [];
      add(p);
    }
    pauseBtn.setAttribute('aria-pressed', String(paused));
    updateCount();
  }

  async function onClear() {
    try {
      await api.clearRequests();
      table.querySelectorAll('tbody').forEach((t) => t.remove());
      rows = 0;
      pending = [];
      updateCount();
    } catch (ex) { fail(ex); }
  }

  function setLive(text, cls) {
    live.textContent = text;
    live.className = `chip ${cls || ''}`;
  }

  async function start() {
    const mine = ++seq;
    es?.close();
    ac?.abort();
    ac = new AbortController();
    table.querySelectorAll('tbody').forEach((t) => t.remove());
    rows = 0;
    maxId = 0;
    pending = [];
    updateCount();
    setLive('connecting');
    const fl = filters();
    try {
      const res = await api.listRequests({ ...fl, limit: 200 }, ac.signal);
      if (mine !== seq) return;
      add(res.entries || []);
    } catch (ex) {
      if (ex.name === 'AbortError') return;
      fail(ex);
    }
    if (mine !== seq) return;
    es = new EventSource(api.streamUrl({ ...fl, since: maxId }));
    es.addEventListener('open', () => setLive('live', 'ok'));
    es.addEventListener('error', () => setLive('reconnecting', 'bad'));
    es.addEventListener('request', (ev) => {
      try { incoming(JSON.parse(ev.data)); } catch { /* ignore malformed event */ }
    });
  }

  const restart = debounce(start, 300);
  Object.values(f).forEach((el) => el.addEventListener(el.type === 'checkbox' ? 'change' : 'input', restart));

  root.append(h('div', { class: 'page reqpage' },
    h('div', { class: 'toolbar wrap' },
      h('div', { class: 'filters' },
        field('Bucket', f.bucket), field('Operation', f.op), field('Key prefix', f.key),
        h('label', { class: 'check', for: 'rq-err' }, f.errors, ' Errors only'),
        h('datalist', { id: 'rq-ops' }, OPS.map((o) => h('option', { value: o })))),
      h('div', { class: 'tools' }, live, count, pauseBtn, clearBtn)),
    h('div', { class: 'tablewrap tall' }, table),
    empty));
  start();

  return {
    destroy() {
      seq++;
      restart.cancel();
      es?.close();
      ac?.abort();
    },
  };
}
