import { h } from './dom.js';

export function toast(kind, text, title) {
  const box = document.getElementById('toasts');
  const el = h('div', { class: `toast ${kind}`, role: kind === 'error' ? 'alert' : 'status' },
    h('div', {}, title && h('strong', {}, title), h('span', {}, text)),
    h('button', { class: 'x', type: 'button', 'aria-label': 'Dismiss', onclick: () => el.remove() }, '×'));
  box.append(el);
  setTimeout(() => el.remove(), kind === 'error' ? 9000 : 4000);
}

const FRIENDLY = {
  BucketAlreadyOwnedByYou: 'A bucket with this name already exists.',
  BucketAlreadyExists: 'A bucket with this name already exists.',
  NoSuchBucket: 'This bucket no longer exists. It may have been deleted or reset.',
  NoSuchKey: 'This object no longer exists. It may have been deleted or reset.',
  BucketNotEmpty: 'This bucket still has objects. Tick "Force" to delete it with everything inside.',
  InvalidBucketName: 'Invalid bucket name. Use 3-63 lowercase letters, digits, dots or hyphens.',
  EntityTooLarge: 'This file is bigger than the server limit (S3LAB_MAX_OBJECT_SIZE).',
  NetworkError: "Can't reach s3lab. Check that the server is running.",
  Unauthorized: 'Your session has expired. Log in again.',
  InternalError: 's3lab hit an internal error. Check the server logs for details.',
};

// errorText turns an API error into a sentence for people; unknown codes fall back to the server message.
export function errorText(err) {
  const known = FRIENDLY[err?.code];
  if (known) return known;
  const msg = String(err?.message || err || 'Something went wrong.').trim();
  const text = msg.charAt(0).toUpperCase() + msg.slice(1);
  return /[.!?]$/.test(text) ? text : `${text}.`;
}

export function fail(err) {
  if (err?.name === 'AbortError') return;
  toast('error', errorText(err));
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
