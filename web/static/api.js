const BASE = '/_s3lab';

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

export function qs(params) {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) {
    if (v !== undefined && v !== null && v !== '' && v !== false) u.set(k, String(v));
  }
  const s = u.toString();
  return s ? `?${s}` : '';
}

async function req(method, path, { params, body, signal } = {}) {
  const init = { method, signal, headers: {} };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    init.headers['Content-Type'] = 'application/json';
  }
  let res;
  try {
    res = await fetch(BASE + path + qs(params), init);
  } catch (e) {
    if (e.name === 'AbortError') throw e;
    throw new ApiError(0, 'NetworkError', 'Cannot reach the s3lab console API');
  }
  if (res.status === 401 && path !== '/login') window.dispatchEvent(new Event('s3lab:unauthorized'));
  if (!res.ok) {
    let code = 'HTTP' + res.status;
    let message = res.statusText;
    try {
      const j = await res.json();
      code = j?.error?.code || code;
      message = j?.error?.message || message;
    } catch { /* non-JSON error body */ }
    throw new ApiError(res.status, code, message);
  }
  if (res.status === 204) return null;
  return res.json();
}

const b = (name) => `/buckets/${encodeURIComponent(name)}`;

export const api = {
  info: (signal) => req('GET', '/info', { signal }),
  login: (accessKey, secretKey) => req('POST', '/login', { body: { accessKey, secretKey } }),
  logout: () => req('POST', '/logout'),
  listBuckets: (signal) => req('GET', '/buckets', { signal }),
  createBucket: (name) => req('POST', '/buckets', { body: { name } }),
  deleteBucket: (name, force) => req('DELETE', b(name), { params: { force: force ? 'true' : '' } }),
  listObjects: (bucket, params, signal) => req('GET', `${b(bucket)}/objects`, { params, signal }),
  getObject: (bucket, key, signal) => req('GET', `${b(bucket)}/object`, { params: { key }, signal }),
  deleteObject: (bucket, key) => req('DELETE', `${b(bucket)}/object`, { params: { key } }),
  deletePrefix: (bucket, prefix) => req('DELETE', `${b(bucket)}/objects`, { params: { prefix } }),
  downloadUrl: (bucket, key, inline) => `${BASE}${b(bucket)}/download${qs({ key, inline: inline ? '1' : '' })}`,
  listRequests: (params, signal) => req('GET', '/requests', { params, signal }),
  clearRequests: () => req('DELETE', '/requests'),
  streamUrl: (params) => `${BASE}/requests/stream${qs(params)}`,
  listFaults: (signal) => req('GET', '/faults', { signal }),
  createFault: (rule) => req('POST', '/faults', { body: rule }),
  deleteFault: (id) => req('DELETE', `/faults/${encodeURIComponent(id)}`),
  deleteAllFaults: () => req('DELETE', '/faults'),
  reset: (params) => req('POST', '/reset', { params }),
};

export function uploadObject(bucket, key, file, onProgress) {
  const xhr = new XMLHttpRequest();
  const promise = new Promise((resolve, reject) => {
    xhr.open('PUT', `${BASE}${b(bucket)}/object${qs({ key })}`);
    xhr.setRequestHeader('Content-Type', file.type || 'application/octet-stream');
    xhr.upload.addEventListener('progress', (e) => e.lengthComputable && onProgress(e.loaded / e.total));
    xhr.addEventListener('load', () => {
      if (xhr.status >= 200 && xhr.status < 300) return resolve();
      if (xhr.status === 401) window.dispatchEvent(new Event('s3lab:unauthorized'));
      let err = new ApiError(xhr.status, 'HTTP' + xhr.status, xhr.statusText);
      try {
        const j = JSON.parse(xhr.responseText);
        err = new ApiError(xhr.status, j.error.code, j.error.message);
      } catch { /* keep generic error */ }
      reject(err);
    });
    xhr.addEventListener('error', () => reject(new ApiError(0, 'NetworkError', 'Upload failed')));
    xhr.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
    xhr.send(file);
  });
  return { promise, abort: () => xhr.abort() };
}
