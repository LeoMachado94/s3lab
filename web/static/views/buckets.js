import { api } from '../api.js';
import { h, fmtDate, stateBox, field } from '../dom.js';
import { toast, fail, dialog } from '../ui.js';

export function validateBucketName(n) {
  if (n.length < 3 || n.length > 63) return 'Must be 3-63 characters';
  if (!/^[a-z0-9.-]+$/.test(n)) return 'Only lowercase letters, digits, dots and hyphens';
  if (!/^[a-z0-9]/.test(n) || !/[a-z0-9]$/.test(n)) return 'Must start and end with a letter or digit';
  if (n.includes('..')) return 'Must not contain consecutive dots';
  if (/^\d+\.\d+\.\d+\.\d+$/.test(n)) return 'Must not look like an IP address';
  if (n.startsWith('xn--') || n.endsWith('-s3alias')) return 'Reserved prefix/suffix';
  return '';
}

export function mount(root) {
  const ac = new AbortController();
  const body = h('div');
  const nameInput = h('input', { id: 'bk-name', type: 'text', placeholder: 'my-bucket', autocomplete: 'off', spellcheck: false, 'aria-describedby': 'bk-err' });
  const err = h('div', { id: 'bk-err', class: 'hint err', role: 'alert' });
  const createBtn = h('button', { class: 'btn primary', type: 'submit' }, 'Create bucket');

  const form = h('form', { class: 'inline-form', novalidate: true, onsubmit: onCreate },
    field('New bucket', nameInput), createBtn);
  nameInput.addEventListener('input', () => {
    err.textContent = nameInput.value ? validateBucketName(nameInput.value) : '';
  });

  async function onCreate(e) {
    e.preventDefault();
    const name = nameInput.value.trim();
    const bad = validateBucketName(name);
    err.textContent = bad;
    if (bad) return;
    createBtn.disabled = true;
    try {
      await api.createBucket(name);
      nameInput.value = '';
      toast('ok', `Bucket ${name} created`);
      load();
    } catch (ex) {
      err.textContent = `${ex.code}: ${ex.message}`;
      fail(ex);
    } finally {
      createBtn.disabled = false;
    }
  }

  async function onDelete(name) {
    const force = h('input', { type: 'checkbox', id: 'bk-force' });
    const ok = await dialog({
      title: `Delete bucket ${name}?`,
      confirmLabel: 'Delete',
      danger: true,
      body: [
        h('p', {}, 'This cannot be undone.'),
        h('label', { class: 'check', for: 'bk-force' }, force, ' Force: delete all objects and uploads first'),
      ],
    });
    if (!ok) return;
    try {
      await api.deleteBucket(name, force.checked);
      toast('ok', `Bucket ${name} deleted`);
      load();
    } catch (ex) { fail(ex); }
  }

  function render(buckets) {
    body.replaceChildren();
    if (!buckets.length) {
      body.append(stateBox('empty', 'No buckets yet. Create one above.'));
      return;
    }
    const rows = buckets.map((bk) => h('tr', {},
      h('td', {}, h('a', { class: 'mono', href: `#/buckets/${encodeURIComponent(bk.name)}` }, bk.name)),
      h('td', {}, fmtDate(bk.createdAt)),
      h('td', { class: 'right' }, h('button', { class: 'btn sm danger-outline', type: 'button', onclick: () => onDelete(bk.name) }, 'Delete'))));
    body.append(h('div', { class: 'tablewrap' }, h('table', {},
      h('thead', {}, h('tr', {}, h('th', {}, 'Name'), h('th', {}, 'Created'), h('th', {}))),
      h('tbody', {}, rows))));
  }

  async function load() {
    try {
      const res = await api.listBuckets(ac.signal);
      render(res.buckets || []);
    } catch (ex) {
      if (ex.name === 'AbortError') return;
      body.replaceChildren(stateBox('error', `${ex.code}: ${ex.message}`, load));
    }
  }

  root.append(h('div', { class: 'page' }, h('h1', {}, 'Buckets'), form, err, body));
  body.append(stateBox('loading', 'Loading buckets...'));
  load();
  return { destroy: () => ac.abort() };
}
