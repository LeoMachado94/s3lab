import { api, uploadObject } from '../api.js';
import { h, fmtSize, fmtDate, stateBox, debounce, copyText } from '../dom.js';
import { toast, fail, dialog, errorText } from '../ui.js';

const PREVIEW_MAX = 1024 * 1024;
const MAX_PARALLEL_UPLOADS = 3;

const hashFor = (bucket, prefix) =>
  `#/buckets/${encodeURIComponent(bucket)}${prefix ? `?prefix=${encodeURIComponent(prefix)}` : ''}`;

export function mount(root, route) {
  const bucket = route.parts[1];
  const st = { prefix: '', filter: '', objects: [], prefixes: [], next: '', truncated: false, selected: null };
  let listAc = null;
  let detailAc = null;
  let loadSeq = 0;
  const uploads = [];

  const crumbs = h('nav', { class: 'crumbs', 'aria-label': 'Path' });
  const filterInput = h('input', { id: 'ob-filter', type: 'search', placeholder: 'filter by prefix', autocomplete: 'off', spellcheck: false });
  const fileInput = h('input', { type: 'file', multiple: true, hidden: true, onchange: () => { enqueue([...fileInput.files]); fileInput.value = ''; } });
  const delPrefixBtn = h('button', { class: 'btn sm danger-outline', type: 'button', onclick: onDeletePrefix }, 'Delete prefix');
  const uploadBtn = h('button', { class: 'btn sm primary', type: 'button', onclick: () => fileInput.click() }, 'Upload');
  const uploadList = h('ul', { class: 'uploads', 'aria-label': 'Uploads' });
  const listBox = h('div', { class: 'listbox' });
  const moreBtn = h('button', { class: 'btn', type: 'button', onclick: () => load(false) }, 'Load more');
  const dropzone = h('div', { class: 'dropzone' }, listBox, h('div', { class: 'dropmsg' }, 'Drop files to upload'));
  const panel = h('aside', { class: 'sidepanel', 'aria-label': 'Object details', hidden: true });

  filterInput.addEventListener('input', debounce(() => {
    st.filter = filterInput.value;
    load(true);
  }, 300));

  const effectivePrefix = () => st.prefix + st.filter;

  function renderCrumbs() {
    crumbs.replaceChildren(h('a', { href: hashFor(bucket, '') }, bucket));
    const segs = st.prefix.split('/').filter(Boolean);
    let acc = '';
    segs.forEach((s) => {
      acc += `${s}/`;
      crumbs.append(h('span', { class: 'sep', 'aria-hidden': 'true' }, '/'), h('a', { href: hashFor(bucket, acc) }, s));
    });
  }

  function renderList() {
    listBox.replaceChildren();
    if (!st.prefixes.length && !st.objects.length) {
      listBox.append(stateBox('empty', st.filter ? 'Nothing matches this prefix.' : 'This location is empty. Drop files here to upload.'));
      return;
    }
    const rows = [];
    for (const p of st.prefixes) {
      rows.push(h('tr', { class: 'folder' },
        h('td', { colspan: 4 }, h('a', { class: 'mono', href: hashFor(bucket, p) }, h('span', { class: 'ico', 'aria-hidden': 'true' }, '▸ '), p.slice(st.prefix.length)))));
    }
    for (const o of st.objects) {
      const btn = h('button', { class: 'link mono', type: 'button', onclick: () => select(o.key), 'aria-pressed': String(o.key === st.selected) }, o.key.slice(st.prefix.length));
      rows.push(h('tr', { class: o.key === st.selected ? 'sel' : '' },
        h('td', {}, btn),
        h('td', { class: 'num' }, fmtSize(o.size)),
        h('td', {}, fmtDate(o.lastModified)),
        h('td', { class: 'mono muted' }, o.contentType)));
    }
    listBox.append(h('div', { class: 'tablewrap' }, h('table', {},
      h('thead', {}, h('tr', {}, h('th', {}, 'Name'), h('th', { class: 'num' }, 'Size'), h('th', {}, 'Modified'), h('th', {}, 'Type'))),
      h('tbody', {}, rows))));
    if (st.truncated) listBox.append(h('div', { class: 'more' }, moreBtn));
  }

  async function load(reset) {
    const seq = ++loadSeq;
    listAc?.abort();
    listAc = new AbortController();
    if (reset) {
      st.objects = [];
      st.prefixes = [];
      st.next = '';
      st.truncated = false;
      listBox.replaceChildren(stateBox('loading', 'Loading...'));
    } else {
      moreBtn.disabled = true;
    }
    try {
      const res = await api.listObjects(bucket, {
        prefix: effectivePrefix(), delimiter: '/', startAfter: reset ? '' : st.next, maxKeys: 200,
      }, listAc.signal);
      if (seq !== loadSeq) return;
      const seen = new Set(st.prefixes);
      for (const p of res.prefixes || []) if (!seen.has(p)) st.prefixes.push(p);
      st.objects.push(...(res.objects || []));
      st.truncated = !!res.isTruncated;
      st.next = res.nextStartAfter || '';
      moreBtn.disabled = false;
      renderList();
    } catch (ex) {
      if (ex.name === 'AbortError' || seq !== loadSeq) return;
      listBox.replaceChildren(stateBox('error', errorText(ex), () => load(reset)));
    }
  }

  function closePanel() {
    detailAc?.abort();
    st.selected = null;
    panel.hidden = true;
    panel.replaceChildren();
    root.firstChild.classList.remove('has-panel');
    renderList();
  }

  async function select(key) {
    st.selected = key;
    detailAc?.abort();
    detailAc = new AbortController();
    const signal = detailAc.signal;
    panel.hidden = false;
    root.firstChild.classList.add('has-panel');
    panel.replaceChildren(panelHead(key), stateBox('loading', 'Loading...'));
    renderList();
    try {
      const meta = await api.getObject(bucket, key, signal);
      panel.replaceChildren(panelHead(key), ...detailBody(meta, signal));
    } catch (ex) {
      if (ex.name === 'AbortError') return;
      panel.replaceChildren(panelHead(key), stateBox('error', errorText(ex), () => select(key)));
    }
  }

  function panelHead(key) {
    return h('div', { class: 'ph' },
      h('h2', { class: 'mono' }, key),
      h('button', { class: 'btn sm', type: 'button', onclick: closePanel }, 'Close'));
  }

  function kv(obj) {
    const entries = Object.entries(obj || {}).filter(([, v]) => v !== '' && v != null);
    if (!entries.length) return h('p', { class: 'muted' }, 'none');
    return h('dl', { class: 'kv' }, entries.map(([k, v]) => [h('dt', {}, k), h('dd', { class: 'mono' }, String(v))]));
  }

  function detailBody(m, signal) {
    const preview = h('div', { class: 'preview' });
    renderPreview(preview, m, signal);
    return [
      h('div', { class: 'pactions' },
        h('a', { class: 'btn sm primary', href: api.downloadUrl(bucket, m.key, false) }, 'Download'),
        h('button', { class: 'btn sm', type: 'button', onclick: async () => toast((await copyText(m.key)) ? 'ok' : 'error', 'Key copied') }, 'Copy key'),
        h('button', { class: 'btn sm danger-outline', type: 'button', onclick: () => onDelete(m.key) }, 'Delete')),
      h('dl', { class: 'kv' },
        h('dt', {}, 'Size'), h('dd', {}, `${fmtSize(m.size)} (${m.size} bytes)`),
        h('dt', {}, 'Modified'), h('dd', {}, fmtDate(m.lastModified)),
        h('dt', {}, 'Content-Type'), h('dd', { class: 'mono' }, m.contentType),
        h('dt', {}, 'ETag'), h('dd', { class: 'mono' }, m.etag),
        h('dt', {}, 'Parts'), h('dd', {}, m.partsCount || 0)),
      h('h3', {}, 'Headers'), kv(m.headers),
      h('h3', {}, 'User metadata'), kv(m.metadata),
      h('h3', {}, 'Preview'), preview,
    ];
  }

  async function renderPreview(box, m, signal) {
    const ct = (m.contentType || '').toLowerCase();
    const textual = /^text\/|json|xml|javascript|yaml/.test(ct);
    if (m.size > PREVIEW_MAX || !(ct.startsWith('image/') || textual)) {
      box.append(h('p', { class: 'muted' }, m.size > PREVIEW_MAX ? 'Too large to preview (limit 1 MB).' : 'No preview for this type.'));
      return;
    }
    const url = api.downloadUrl(bucket, m.key, true);
    if (ct.startsWith('image/')) {
      box.append(h('img', { src: url, alt: `Preview of ${m.key}` }));
      return;
    }
    box.append(stateBox('loading', 'Loading preview...'));
    try {
      const res = await fetch(url, { signal });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      let text = await res.text();
      if (ct.includes('json')) {
        try { text = JSON.stringify(JSON.parse(text), null, 2); } catch { /* show raw */ }
      }
      box.replaceChildren(h('pre', { class: 'json' }, text));
    } catch (ex) {
      if (ex.name !== 'AbortError') box.replaceChildren(h('p', { class: 'muted' }, `Preview failed: ${ex.message}`));
    }
  }

  async function onDelete(key) {
    const ok = await dialog({ title: 'Delete object?', confirmLabel: 'Delete', danger: true, body: h('p', { class: 'mono wrap' }, key) });
    if (!ok) return;
    try {
      await api.deleteObject(bucket, key);
      st.objects = st.objects.filter((o) => o.key !== key);
      closePanel();
      toast('ok', 'Object deleted');
    } catch (ex) { fail(ex); }
  }

  async function onDeletePrefix() {
    const p = effectivePrefix();
    const ok = await dialog({
      title: 'Delete prefix?',
      confirmLabel: 'Delete all',
      danger: true,
      body: [
        h('p', {}, p ? 'Every object whose key starts with:' : 'No prefix set: this deletes EVERY object in the bucket.'),
        p && h('p', { class: 'mono wrap' }, `${bucket}/${p}`),
      ],
    });
    if (!ok) return;
    try {
      const res = await api.deletePrefix(bucket, p);
      toast('ok', `${res?.deleted ?? 0} objects deleted`);
      closePanel();
      load(true);
    } catch (ex) { fail(ex); }
  }

  function enqueue(files) {
    if (!uploads.length) uploadList.replaceChildren();
    for (const file of files) {
      const item = { file, key: st.prefix + file.name, state: 'queued', pct: 0, abort: null };
      item.progress = h('progress', { max: 1, value: 0, 'aria-label': `Upload ${file.name}` });
      item.status = h('span', { class: 'muted' }, 'queued');
      item.cancel = h('button', { class: 'btn sm', type: 'button', onclick: () => item.abort?.() || cancelQueued(item) }, 'Cancel');
      item.el = h('li', {}, h('span', { class: 'mono name' }, item.key), item.progress, item.status, item.cancel);
      uploads.push(item);
      uploadList.append(item.el);
    }
    pump();
  }

  function cancelQueued(item) {
    if (item.state !== 'queued') return;
    item.state = 'cancelled';
    item.status.textContent = 'cancelled';
    item.cancel.remove();
  }

  function pump() {
    let running = uploads.filter((u) => u.state === 'running').length;
    for (const item of uploads) {
      if (running >= MAX_PARALLEL_UPLOADS) break;
      if (item.state !== 'queued') continue;
      running++;
      runUpload(item);
    }
    if (running === 0 && uploads.some((u) => u.state === 'done')) {
      uploads.length = 0;
      load(true);
    }
  }

  async function runUpload(item) {
    item.state = 'running';
    item.status.textContent = '0%';
    const up = uploadObject(bucket, item.key, item.file, (f) => {
      item.progress.value = f;
      item.status.textContent = `${Math.round(f * 100)}%`;
    });
    item.abort = () => { up.abort(); return true; };
    try {
      await up.promise;
      item.state = 'done';
      item.progress.value = 1;
      item.status.textContent = 'done';
      item.status.className = 'ok-text';
    } catch (ex) {
      item.state = 'failed';
      item.status.textContent = ex.name === 'AbortError' ? 'cancelled' : errorText(ex);
      item.status.className = 'err';
      if (ex.name !== 'AbortError') fail(ex);
    }
    item.cancel.remove();
    pump();
  }

  ['dragenter', 'dragover'].forEach((t) => dropzone.addEventListener(t, (e) => {
    e.preventDefault();
    dropzone.classList.add('drag');
  }));
  ['dragleave', 'drop'].forEach((t) => dropzone.addEventListener(t, (e) => {
    e.preventDefault();
    if (t === 'drop' || e.target === dropzone) dropzone.classList.remove('drag');
  }));
  dropzone.addEventListener('drop', (e) => enqueue([...(e.dataTransfer?.files || [])]));

  function applyRoute(r) {
    const prefix = r.query.get('prefix') || '';
    if (prefix === st.prefix && loadSeq > 0) return;
    st.prefix = prefix;
    st.filter = '';
    filterInput.value = '';
    renderCrumbs();
    if (st.selected) closePanel();
    load(true);
  }

  root.append(h('div', { class: 'page objects' },
    h('div', { class: 'main' },
      h('div', { class: 'toolbar' }, crumbs,
        h('div', { class: 'tools' },
          h('label', { class: 'sr-only', for: 'ob-filter' }, 'Filter by prefix'), filterInput,
          uploadBtn, fileInput, delPrefixBtn)),
      uploadList, dropzone),
    panel));
  applyRoute(route);

  return { destroy: () => { listAc?.abort(); detailAc?.abort(); uploads.forEach((u) => u.abort?.()); }, update: applyRoute };
}
