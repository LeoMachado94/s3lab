import { api } from '../api.js';
import { h, stateBox, field } from '../dom.js';
import { toast, fail, dialog } from '../ui.js';
import { OPS } from '../ops.js';

const PRESETS = [
  ['503 SlowDown on PutObject', { op: 'PutObject', status: 503, code: 'SlowDown', message: 'Please reduce your request rate.' }],
  ['500 on GetObject 50%', { op: 'GetObject', status: 500, probability: 0.5 }],
  ['2s latency on everything', { delayMs: 2000 }],
  ['Throttle first 3 requests', { status: 503, code: 'SlowDown', message: 'Please reduce your request rate.', count: 3 }],
];

export function mount(root) {
  const ac = new AbortController();
  const listBox = h('div');
  let lastErr = '';

  const inputs = {
    target: h('select', { id: 'f-target' }, h('option', { value: 'http' }, 'http'), h('option', { value: 'storage' }, 'storage')),
    op: h('input', { id: 'f-op', type: 'text', list: 'f-ops', placeholder: 'any', autocomplete: 'off' }),
    bucket: h('input', { id: 'f-bucket', type: 'text', placeholder: 'any', autocomplete: 'off' }),
    key: h('input', { id: 'f-key', type: 'text', placeholder: 'glob, e.g. uploads/*', autocomplete: 'off' }),
    status: h('input', { id: 'f-status', type: 'number', min: 0, max: 599, placeholder: '0 = delay only' }),
    code: h('input', { id: 'f-code', type: 'text', placeholder: 'derived from status', autocomplete: 'off' }),
    message: h('input', { id: 'f-msg', type: 'text', autocomplete: 'off' }),
    delayMs: h('input', { id: 'f-delay', type: 'number', min: 0, placeholder: '0' }),
    probability: h('input', { id: 'f-prob', type: 'number', min: 0, max: 1, step: 'any', placeholder: '1' }),
    count: h('input', { id: 'f-count', type: 'number', min: 0, placeholder: '0 = unlimited' }),
  };
  const submit = h('button', { class: 'btn primary', type: 'submit' }, 'Add rule');
  const labels = { target: 'Target', op: 'Operation', bucket: 'Bucket', key: 'Key glob', status: 'Status', code: 'Error code', message: 'Message', delayMs: 'Delay (ms)', probability: 'Probability (0-1]', count: 'Count' };
  const form = h('form', { class: 'faultform', novalidate: true, onsubmit: onSubmit },
    Object.entries(inputs).map(([k, el]) => field(labels[k], el)),
    h('datalist', { id: 'f-ops' }, OPS.map((o) => h('option', { value: o }))),
    h('div', { class: 'formactions' }, submit));

  function buildRule() {
    const rule = { target: inputs.target.value };
    for (const k of ['op', 'bucket', 'key', 'code', 'message']) {
      const v = inputs[k].value.trim();
      if (v) rule[k] = v;
    }
    for (const k of ['status', 'delayMs', 'count']) {
      if (inputs[k].value !== '') rule[k] = parseInt(inputs[k].value, 10);
    }
    if (inputs.probability.value !== '') rule.probability = parseFloat(inputs.probability.value);
    return rule;
  }

  async function create(rule) {
    try {
      await api.createFault(rule);
      toast('ok', 'Fault rule added');
      load();
      return true;
    } catch (ex) {
      fail(ex);
      return false;
    }
  }

  async function onSubmit(e) {
    e.preventDefault();
    const rule = buildRule();
    if (rule.probability !== undefined && !(rule.probability > 0 && rule.probability <= 1)) {
      toast('error', 'Probability must be in (0, 1]', 'Invalid rule');
      return;
    }
    if ([rule.status, rule.delayMs, rule.count].some(Number.isNaN)) {
      toast('error', 'Numeric fields must be numbers', 'Invalid rule');
      return;
    }
    submit.disabled = true;
    await create(rule);
    submit.disabled = false;
  }

  async function onDelete(id) {
    try {
      await api.deleteFault(id);
      load();
    } catch (ex) { fail(ex); }
  }

  async function onDeleteAll() {
    const ok = await dialog({ title: 'Delete all fault rules?', confirmLabel: 'Delete all', danger: true, body: h('p', {}, 'Every rule will be removed.') });
    if (!ok) return;
    try {
      await api.deleteAllFaults();
      load();
    } catch (ex) { fail(ex); }
  }

  function render(rules) {
    listBox.replaceChildren();
    if (!rules.length) {
      listBox.append(stateBox('empty', 'No fault rules. Add one below or use a preset.'));
      return;
    }
    const cell = (v, cls) => h('td', { class: cls || '' }, v === undefined || v === '' || v === null ? '' : String(v));
    const rows = rules.map((r) => h('tr', {},
      cell(r.id, 'mono'), cell(r.target || 'http'), cell(r.op, 'mono'), cell(r.bucket, 'mono'), cell(r.key, 'mono'),
      cell(r.status ? `${r.status} ${r.code || ''}`.trim() : ''), cell(r.delayMs || ''),
      cell(r.probability && r.probability < 1 ? r.probability : ''),
      cell(r.count ? `${r.hits || 0} / ${r.count}` : `${r.hits || 0}`, 'num'),
      h('td', { class: 'right' }, h('button', { class: 'btn sm danger-outline', type: 'button', 'aria-label': `Delete rule ${r.id}`, onclick: () => onDelete(r.id) }, 'Delete'))));
    listBox.append(h('div', { class: 'tablewrap' }, h('table', {},
      h('thead', {}, h('tr', {}, ['ID', 'Target', 'Op', 'Bucket', 'Key', 'Status', 'Delay ms', 'Prob.', 'Hits', ''].map((t) => h('th', {}, t)))),
      h('tbody', {}, rows))));
  }

  async function load(silent) {
    try {
      const res = await api.listFaults(ac.signal);
      lastErr = '';
      render(res.rules || []);
    } catch (ex) {
      if (ex.name === 'AbortError') return;
      if (silent === true) {
        const msg = `${ex.code}: ${ex.message}`;
        if (msg !== lastErr) fail(ex);
        lastErr = msg;
        return;
      }
      listBox.replaceChildren(stateBox('error', `${ex.code}: ${ex.message}`, load));
    }
  }

  const timer = setInterval(() => { if (!document.hidden) load(true); }, 3000);

  root.append(h('div', { class: 'page' },
    h('div', { class: 'toolbar' }, h('h1', {}, 'Fault injection'),
      h('div', { class: 'tools' }, h('button', { class: 'btn sm danger-outline', type: 'button', onclick: onDeleteAll }, 'Delete all'))),
    listBox,
    h('h2', {}, 'Presets'),
    h('div', { class: 'presets' }, PRESETS.map(([label, rule]) => h('button', { class: 'btn', type: 'button', onclick: () => create({ target: 'http', ...rule }) }, label))),
    h('h2', {}, 'New rule'),
    form));
  listBox.append(stateBox('loading', 'Loading rules...'));
  load();

  return { destroy: () => { clearInterval(timer); ac.abort(); } };
}
