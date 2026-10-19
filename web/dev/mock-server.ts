// Mock of docs/admin-api.md for UI development.
// docker run --rm -p 9001:9001 -v "$PWD/web":/web -w /web oven/bun:1 bun dev/mock-server.ts
import { join, normalize } from "node:path";

const PORT = Number(process.env.PORT ?? 9001);
const STATIC = join(import.meta.dir, "..", "static");
const now = () => new Date().toISOString();

type Obj = { data: Uint8Array; contentType: string; metadata: Record<string, string>; lastModified: string };
const enc = new TextEncoder();
const buckets = new Map<string, { createdAt: string; objects: Map<string, Obj> }>();
const put = (b: string, key: string, body: string | Uint8Array, contentType: string, metadata = {}) =>
  buckets.get(b)!.objects.set(key, { data: typeof body === "string" ? enc.encode(body) : body, contentType, metadata, lastModified: now() });

function seed() {
  buckets.clear();
  for (const n of ["photos", "docs"]) buckets.set(n, { createdAt: now(), objects: new Map() });
  put("photos", "readme.txt", "hello from s3lab\n", "text/plain", { owner: "leo" });
  put("photos", "2026/config.json", '{"a":1,"b":[1,2,3]}', "application/json");
  put("photos", "2026/trip/a<script>.txt", "<b>not html</b>", "text/plain");
  for (let i = 0; i < 250; i++) put("photos", `bulk/file-${String(i).padStart(3, "0")}.bin`, new Uint8Array(i * 100), "application/octet-stream");
  const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><circle cx="40" cy="40" r="30" fill="tomato"/></svg>';
  put("photos", "logo.svg", svg, "image/svg+xml");
}
seed();

type Rule = Record<string, any>;
let rules: Rule[] = [];
let faultSeq = 0;

type Entry = Record<string, any>;
let log: Entry[] = [];
let nextId = 1;
const clients = new Set<{ send: (s: string) => void; match: (e: Entry) => boolean }>();

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const err = (status: number, code: string, message: string) => json({ error: { code, message } }, status);

function matcher(q: URLSearchParams) {
  return (e: Entry) =>
    (!q.get("bucket") || e.bucket === q.get("bucket")) &&
    (!q.get("op") || e.op === q.get("op")) &&
    (!q.get("key") || (e.key ?? "").startsWith(q.get("key")!)) &&
    (q.get("errors") !== "true" || e.status >= 400);
}

const OPS = ["PutObject", "GetObject", "HeadObject", "DeleteObject", "ListObjectsV2", "ListBuckets", "CreateBucket"];
function fake(): Entry {
  const op = OPS[Math.floor(Math.random() * OPS.length)];
  const bucket = Math.random() < 0.8 ? "photos" : "docs";
  const status = Math.random() < 0.2 ? [404, 500, 503, 403][Math.floor(Math.random() * 4)] : 200;
  const fault = status === 503 ? "slow-puts" : "";
  const e: Entry = {
    id: nextId++, requestId: Math.random().toString(16).slice(2, 18).toUpperCase(), time: now(),
    method: op.startsWith("Put") ? "PUT" : op.startsWith("Delete") ? "DELETE" : op.startsWith("Head") ? "HEAD" : "GET",
    path: `/${bucket}/k${nextId}.jpg`, op, bucket, key: `k${nextId}.jpg`, status,
    errorCode: status >= 400 ? { 404: "NoSuchKey", 500: "InternalError", 503: "SlowDown", 403: "AccessDenied" }[status] : "",
    durationMs: Math.random() * 20, bytesIn: op === "PutObject" ? Math.floor(Math.random() * 90000) : 0,
    bytesOut: op === "GetObject" ? Math.floor(Math.random() * 90000) : 0, accessKey: "s3lab", auth: "header", faultId: fault,
    remoteAddr: "172.18.0.1:53422", userAgent: "aws-sdk-go-v2/1.30.0",
  };
  if (op === "ListBuckets") { delete e.bucket; delete e.key; e.path = "/"; }
  return e;
}
setInterval(() => {
  const e = fake();
  log.push(e);
  if (log.length > 1000) log.shift();
  const msg = `event: request\nid: ${e.id}\ndata: ${JSON.stringify(e)}\n\n`;
  for (const c of clients) if (c.match(e)) c.send(msg);
}, 1000);
for (let i = 0; i < 30; i++) log.push(fake());

function objMeta(key: string, o: Obj) {
  return {
    key, size: o.data.length, etag: "9e107d9d372bb6826bd81d3542a419d6", lastModified: o.lastModified, partsCount: 0,
    contentType: o.contentType, headers: { "Cache-Control": "max-age=60" }, metadata: o.metadata,
  };
}

async function api(req: Request, url: URL): Promise<Response> {
  const p = url.pathname.slice("/_s3lab".length);
  const q = url.searchParams;
  const m = req.method;
  if (p === "/health") return json({ status: "ok", version: "0.1.0" });
  if (p === "/info") return json({ version: "0.1.0", region: "us-east-1", storage: "memory", s3Port: 9000, consolePort: PORT, accessKey: "s3lab", virtualHostDomain: "", requestLogCapacity: 1000, startedAt: now() });

  if (p === "/buckets") {
    if (m === "GET") return json({ buckets: [...buckets].map(([name, b]) => ({ name, createdAt: b.createdAt })) });
    if (m === "POST") {
      const { name } = await req.json();
      if (!/^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/.test(name ?? "")) return err(400, "InvalidBucketName", "The specified bucket is not valid.");
      if (buckets.has(name)) return err(409, "BucketAlreadyOwnedByYou", "Your previous request to create the named bucket succeeded.");
      buckets.set(name, { createdAt: now(), objects: new Map() });
      return json({ name, createdAt: buckets.get(name)!.createdAt }, 201);
    }
  }
  let mm = p.match(/^\/buckets\/([^/]+)(?:\/(objects|object|download))?$/);
  if (mm) {
    const name = decodeURIComponent(mm[1]);
    const b = buckets.get(name);
    if (!b) return err(404, "NoSuchBucket", "The specified bucket does not exist");
    const sub = mm[2];
    const key = q.get("key") ?? "";
    if (!sub && m === "DELETE") {
      if (b.objects.size && q.get("force") !== "true") return err(409, "BucketNotEmpty", "The bucket you tried to delete is not empty");
      buckets.delete(name);
      return new Response(null, { status: 204 });
    }
    if (sub === "objects" && m === "GET") {
      const prefix = q.get("prefix") ?? "";
      const delim = q.has("delimiter") ? q.get("delimiter")! : "/";
      const after = q.get("startAfter") ?? "";
      const max = Math.min(Number(q.get("maxKeys") ?? 200), 1000);
      const keys = [...b.objects.keys()].sort().filter((k) => k.startsWith(prefix) && k > after);
      const objects: any[] = [];
      const prefixes: string[] = [];
      let last = "";
      let truncated = false;
      for (const k of keys) {
        const rest = k.slice(prefix.length);
        const i = delim ? rest.indexOf(delim) : -1;
        const cp = i >= 0 ? prefix + rest.slice(0, i + 1) : "";
        if (cp && prefixes.includes(cp)) { last = k; continue; }
        if (objects.length + prefixes.length >= max) { truncated = true; break; }
        if (cp) prefixes.push(cp);
        else { const o = b.objects.get(k)!; objects.push({ key: k, size: o.data.length, etag: "9e107d9d372bb6826bd81d3542a419d6", lastModified: o.lastModified, contentType: o.contentType }); }
        last = k;
      }
      return json({ objects, prefixes, isTruncated: truncated, nextStartAfter: truncated ? last : "" });
    }
    if (sub === "objects" && m === "DELETE") {
      const prefix = q.get("prefix") ?? "";
      let n = 0;
      for (const k of [...b.objects.keys()]) if (k.startsWith(prefix)) { b.objects.delete(k); n++; }
      return json({ deleted: n });
    }
    if (sub === "object" || sub === "download") {
      if (m === "PUT") {
        const meta: Record<string, string> = {};
        req.headers.forEach((v, k) => { if (k.startsWith("x-amz-meta-")) meta[k.slice(11)] = v; });
        b.objects.set(key, { data: new Uint8Array(await req.arrayBuffer()), contentType: req.headers.get("content-type") ?? "application/octet-stream", metadata: meta, lastModified: now() });
        return json(objMeta(key, b.objects.get(key)!), 201);
      }
      const o = b.objects.get(key);
      if (!o) return err(404, "NoSuchKey", "The specified key does not exist.");
      if (sub === "object" && m === "GET") return json(objMeta(key, o));
      if (sub === "object" && m === "DELETE") { b.objects.delete(key); return new Response(null, { status: 204 }); }
      if (sub === "download") {
        const disp = q.get("inline") === "1" ? "inline" : "attachment";
        return new Response(o.data, { headers: { "Content-Type": o.contentType, "Content-Disposition": `${disp}; filename="${key.split("/").pop()}"` } });
      }
    }
  }

  if (p === "/requests") {
    if (m === "DELETE") { log = []; return new Response(null, { status: 204 }); }
    const since = Number(q.get("since") ?? 0);
    const limit = Number(q.get("limit") ?? 100);
    return json({ entries: log.filter((e) => e.id > since).filter(matcher(q)).slice(-limit) });
  }
  if (p === "/requests/stream") {
    const match = matcher(q);
    let client: any;
    let ping: any;
    const stream = new ReadableStream({
      start(c) {
        client = { match, send: (s: string) => c.enqueue(enc.encode(s)) };
        clients.add(client);
        c.enqueue(enc.encode(": ok\n\n"));
        ping = setInterval(() => c.enqueue(enc.encode(": ping\n\n")), 15000);
      },
      cancel() { clients.delete(client); clearInterval(ping); },
    });
    return new Response(stream, { headers: { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" } });
  }

  if (p === "/faults") {
    if (m === "GET") return json({ rules });
    if (m === "DELETE") { rules = []; return new Response(null, { status: 204 }); }
    if (m === "POST") {
      const r = await req.json();
      if (r.status && (r.status < 100 || r.status > 599)) return err(400, "BadRequest", "status out of range");
      if (!r.id) r.id = `rule-${++faultSeq}`;
      if (rules.some((x) => x.id === r.id)) return err(409, "BadRequest", "duplicate id");
      rules.push({ target: "http", count: 0, ...r, hits: 0 });
      return json(rules[rules.length - 1], 201);
    }
  }
  mm = p.match(/^\/faults\/([^/]+)$/);
  if (mm && m === "DELETE") {
    const id = decodeURIComponent(mm[1]);
    if (!rules.some((r) => r.id === id)) return err(404, "NotFound", "no such rule");
    rules = rules.filter((r) => r.id !== id);
    return new Response(null, { status: 204 });
  }
  if (p === "/reset" && m === "POST") {
    seed();
    if (q.get("requests") === "true") log = [];
    if (q.get("faults") === "true") rules = [];
    return new Response(null, { status: 204 });
  }
  return err(404, "NotFound", `no route ${m} ${p}`);
}

setInterval(() => rules.forEach((r) => { if (Math.random() < 0.5) r.hits++; }), 1000);

Bun.serve({
  port: PORT,
  idleTimeout: 0,
  async fetch(req) {
    const url = new URL(req.url);
    if (url.pathname.startsWith("/_s3lab/")) return api(req, url);
    const rel = normalize(url.pathname).replace(/^(\.\.[/\\])+/, "");
    const file = Bun.file(join(STATIC, rel));
    if (rel !== "/" && (await file.exists())) return new Response(file);
    return new Response(Bun.file(join(STATIC, "index.html")));
  },
});
console.log(`mock s3lab console on :${PORT}`);
