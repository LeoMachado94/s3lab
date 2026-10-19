# Web console

Open <http://localhost:9001> and log in with the S3 access key and secret key (`S3LAB_ACCESS_KEY` / `S3LAB_SECRET_KEY`, default `s3lab` / `s3labsecret`). The session lasts 12 hours or until s3lab restarts; **Log out** is in the header. The console talks only to the [admin API](admin-api.md), so everything you can do here you can also script.

## Header

- **Version, storage backend and region** of the running server.
- **Connect**: copy-ready endpoint (the host comes from your browser address), access key, a placeholder for the secret key (never the real one), region and a reminder to enable path-style addressing. Paste straight into your `.env`.
- **Reset**: wipes all data and re-applies the seed. *requests* is ticked by default and *faults* is not; they also clear the request log and fault rules. Reset is global: a per-bucket reset is only available through the [API](admin-api.md#reset).
- **Theme toggle**: cycles auto (follows your OS) → light → dark.

## Buckets — `#/buckets`

- List of buckets with creation date.
- **Create bucket**: names are validated against S3 rules (3–63 chars, lowercase, digits, `-`, `.`).
- **Delete**: refuses non-empty buckets unless you tick *force* (deletes objects and pending uploads first).

## Objects — `#/buckets/<name>`

- Folder-style navigation: prefixes ending in `/` are shown as folders, with a breadcrumb to go back.
- **Filter** by prefix, **Load more** for large buckets.
- **Upload**: pick files or drag & drop them onto the list; they go to the current folder, with per-file progress.
- **Object panel** (click an object): size, ETag, last modified, parts count, content type, system headers (`Cache-Control`, `Content-Disposition`, …), user metadata (`x-amz-meta-*`), preview for images/text/JSON up to 1 MB, **Copy key**, **Download** and **Delete**.
- **Delete prefix**: deletes every object under the current prefix; at the bucket root it empties the bucket.

## Requests — `#/requests`

The live log of every S3 request your applications send.

- New requests stream in as they happen (Server-Sent Events).
- Columns: time, method, operation, bucket/key, status (colour-coded), error code, duration, bytes in/out, and a badge when a fault rule fired.
- **Filters**: bucket, operation, key prefix, errors only.
- **Pause** to freeze the view while traffic keeps being recorded; **Clear** empties the log.
- Click a row to see the full entry as JSON (request ID, user agent, auth method, remote address…).

Typical uses: see why a `SignatureDoesNotMatch` happens (wrong host or path-style setting), check how many calls a page load makes, confirm that multipart kicks in for large uploads.

## Faults — `#/faults`

- Rules with their match conditions, effect and **hits** (refreshes every 3 s).
- **Presets**: one click to add common scenarios (503 SlowDown on PutObject, 50% failing GetObject, 2 s latency on everything, throttle the first 3 requests).
- **Custom rule** form with every field except `id` from the [rule reference](admin-api.md#fault-injection).
- Delete one rule or all of them.

## Disabling the console

`S3LAB_CONSOLE_ENABLED=false` turns off the UI **and** the admin API. Only the S3 port stays up.
