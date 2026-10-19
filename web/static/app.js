import { api } from './api.js';
import { h, copyText, field } from './dom.js';
import { app, storageGet, storageSet } from './store.js';
import { toast, fail, dialog, errorText } from './ui.js';
import * as bucketsView from './views/buckets.js';
import * as objectsView from './views/objects.js';
import * as requestsView from './views/requests.js';
import * as faultsView from './views/faults.js';

const THEMES = ['auto', 'light', 'dark'];
const $ = (id) => document.getElementById(id);

function applyTheme(t) {
  if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
}

function initHeader() {
  let theme = THEMES.includes(storageGet('s3lab.theme')) ? storageGet('s3lab.theme') : 'auto';
  applyTheme(theme);
  const themeBtn = h('button', { class: 'btn sm', type: 'button', title: 'Toggle theme' }, `Theme: ${theme}`);
  themeBtn.addEventListener('click', () => {
    theme = THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
    storageSet('s3lab.theme', theme);
    applyTheme(theme);
    themeBtn.textContent = `Theme: ${theme}`;
  });
  const connectBtn = h('button', { class: 'btn sm', type: 'button', 'aria-expanded': 'false', 'aria-controls': 'connect' }, 'Connect');
  connectBtn.addEventListener('click', () => {
    const panel = $('connect');
    panel.hidden = !panel.hidden;
    connectBtn.setAttribute('aria-expanded', String(!panel.hidden));
  });
  const resetBtn = h('button', { class: 'btn sm danger-outline', type: 'button', onclick: confirmReset }, 'Reset');
  const logoutBtn = h('button', { class: 'btn sm', type: 'button', id: 'logout', hidden: true, onclick: logout }, 'Log out');
  $('actions').append(connectBtn, resetBtn, themeBtn, logoutBtn);
}

async function logout() {
  try { await api.logout(); } catch (e) { fail(e); return; }
  location.reload();
}

let locked = false;

function showLogin() {
  if (locked) return;
  locked = true;
  current?.destroy?.();
  current = null;
  document.body.classList.add('locked');
  $('connect').hidden = true;
  const ak = h('input', { type: 'text', name: 'username', autocomplete: 'username', required: true, autofocus: true });
  const sk = h('input', { type: 'password', name: 'password', autocomplete: 'current-password', required: true });
  const msg = h('p', { class: 'hint err', role: 'alert' });
  const submit = h('button', { class: 'btn primary', type: 'submit' }, 'Log in');
  const form = h('form', { class: 'login', onsubmit: async (ev) => {
    ev.preventDefault();
    submit.disabled = true;
    msg.textContent = '';
    try {
      await api.login(ak.value, sk.value);
      location.reload();
    } catch (e) {
      msg.textContent = e.status === 401 ? 'Wrong access key or secret key.' : errorText(e);
      submit.disabled = false;
      sk.select();
    }
  } },
  h('h2', {}, 'Log in to s3lab'),
  h('p', { class: 'hint' }, 'Use the S3 access key and secret key (S3LAB_ACCESS_KEY / S3LAB_SECRET_KEY).'),
  field('Access key', ak), field('Secret key', sk), msg, submit);
  $('view').replaceChildren(form);
  ak.focus();
}

function renderMeta(info) {
  const meta = $('meta');
  meta.replaceChildren();
  if (!info) {
    meta.append(h('span', { class: 'chip bad' }, 'API unreachable'));
    return;
  }
  const chip = (k, v) => h('span', { class: 'chip' }, h('span', { class: 'k' }, k), ' ', h('span', { class: 'mono' }, v));
  meta.append(
    chip('v', info.version), chip('storage', info.storage), chip('region', info.region),
    chip('s3', `:${info.s3Port}`),
  );
}

function copyBtn(value, label) {
  const btn = h('button', { class: 'btn sm', type: 'button' }, 'Copy');
  btn.addEventListener('click', async () => {
    const ok = await copyText(value);
    toast(ok ? 'ok' : 'error', ok ? `${label} copied` : 'Copy failed');
  });
  return btn;
}

function copyRow(label, value) {
  return h('div', { class: 'copyrow' }, h('span', { class: 'k' }, label), h('code', {}, value), copyBtn(value, label));
}

function renderConnect(info) {
  const box = $('connect');
  box.replaceChildren();
  if (!info) return;
  const endpoint = `http://${location.hostname}:${info.s3Port}`;
  const env = [
    `AWS_ACCESS_KEY_ID=${info.accessKey}`,
    'AWS_SECRET_ACCESS_KEY=<your secret key>',
    `AWS_REGION=${info.region}`,
    `AWS_ENDPOINT_URL=${endpoint}`,
  ].join('\n');
  const cli = `aws --endpoint-url ${endpoint} s3 ls`;
  box.append(
    copyRow('Endpoint', endpoint),
    copyRow('Access key', info.accessKey),
    copyRow('Region', info.region),
    copyRow('CLI', cli),
    h('div', { class: 'copyrow top' }, h('span', { class: 'k' }, 'Env'), h('pre', {}, env), copyBtn(env, 'Env')),
    h('p', { class: 'hint' }, 'Use path-style addressing (forcePathStyle / s3.addressing_style=path).',
      info.virtualHostDomain && ` Virtual-host domain: ${info.virtualHostDomain}.`),
  );
}

async function confirmReset() {
  const reqs = h('input', { type: 'checkbox', checked: true, id: 'rs-req' });
  const flts = h('input', { type: 'checkbox', id: 'rs-flt' });
  const ok = await dialog({
    title: 'Reset s3lab?',
    confirmLabel: 'Reset',
    danger: true,
    body: [
      h('p', {}, 'Deletes all buckets, objects and uploads, then re-applies the seed.'),
      h('label', { class: 'check', for: 'rs-req' }, reqs, ' Also clear the request log'),
      h('label', { class: 'check', for: 'rs-flt' }, flts, ' Also remove fault rules'),
    ],
  });
  if (!ok) return;
  try {
    await api.reset({ requests: reqs.checked ? 'true' : '', faults: flts.checked ? 'true' : '' });
    toast('ok', 'Reset complete');
    route(true);
  } catch (e) { fail(e); }
}

async function loadInfo() {
  try {
    app.set({ info: await api.info() });
    return true;
  } catch (e) {
    app.set({ info: null });
    if (e.status === 401) return false;
    fail(e);
    return true;
  }
}

function parseHash() {
  const raw = location.hash.slice(1) || '/buckets';
  const [path, q = ''] = raw.split('?');
  const parts = path.split('/').filter(Boolean).map(decodeURIComponent);
  return { parts, query: new URLSearchParams(q) };
}

let current = null;
let currentKey = '';

function route(force) {
  const r = parseHash();
  let view;
  let key;
  switch (r.parts[0]) {
    case 'buckets':
      view = r.parts[1] ? objectsView : bucketsView;
      key = r.parts[1] ? `objects:${r.parts[1]}` : 'buckets';
      break;
    case 'requests': view = requestsView; key = 'requests'; break;
    case 'faults': view = faultsView; key = 'faults'; break;
    default: location.hash = '#/buckets'; return;
  }
  document.querySelectorAll('[data-nav]').forEach((a) => {
    if (a.dataset.nav === r.parts[0]) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  });
  if (locked) return;
  if (!force && current && key === currentKey && current.update) {
    current.update(r);
    return;
  }
  current?.destroy?.();
  const main = $('view');
  main.replaceChildren();
  current = view.mount(main, r);
  currentKey = key;
}

initHeader();
app.subscribe(({ info }) => {
  renderMeta(info);
  renderConnect(info);
  $('logout').hidden = !info?.auth;
});
window.addEventListener('s3lab:unauthorized', showLogin);
window.addEventListener('hashchange', () => route(false));
if (await loadInfo()) route(true);
