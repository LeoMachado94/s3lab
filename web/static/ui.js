import { h } from './dom.js';

export function toast(kind, text, title) {
  const box = document.getElementById('toasts');
  const el = h('div', { class: `toast ${kind}`, role: kind === 'error' ? 'alert' : 'status' },
    h('div', {}, title && h('strong', {}, title), h('span', {}, text)),
    h('button', { class: 'x', type: 'button', 'aria-label': 'Dismiss', onclick: () => el.remove() }, '×'));
  box.append(el);
  setTimeout(() => el.remove(), kind === 'error' ? 9000 : 4000);
}

export function fail(err) {
  if (err?.name === 'AbortError') return;
  toast('error', err?.message || String(err), err?.code);
}

let dlgSeq = 0;

export function dialog({ title, body, confirmLabel = 'Confirm', cancelLabel = 'Cancel', danger = false }) {
  return new Promise((resolve) => {
    const id = `dlg-${++dlgSeq}`;
    let result = false;
    const dlg = h('dialog', { class: 'dlg', 'aria-labelledby': id });
    const finish = (v) => { result = v; dlg.close(); };
    dlg.append(
      h('h2', { id }, title),
      h('div', { class: 'dlg-body' }, body),
      h('div', { class: 'dlg-actions' },
        h('button', { class: 'btn', type: 'button', onclick: () => finish(false) }, cancelLabel),
        h('button', { class: `btn ${danger ? 'danger' : 'primary'}`, type: 'button', onclick: () => finish(true) }, confirmLabel)),
    );
    dlg.addEventListener('close', () => { dlg.remove(); resolve(result); });
    document.body.append(dlg);
    dlg.showModal();
  });
}
