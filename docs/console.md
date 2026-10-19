# Web console

Open <http://localhost:9001>. The console talks only to the [admin API](admin-api.md), so everything you can do here you can also script.

## Header

- **Version, storage backend and region** of the running server.
- **Connect**: copy-ready endpoint, access key, region and a reminder to enable path-style addressing. Paste straight into your `.env`.
- **Reset**: wipes all data and re-applies the seed. Tick *requests* and/or *faults* to also clear the request log and fault rules.
- **Theme toggle**: light / dark (follows your OS by default).

## Buckets — `#/buckets`

- List of buckets with creation date.
- **Create bucket**: names are validated against S3 rules (3–63 chars, lowercase, digits, `-`, `.`).
- **Delete**: refuses non-empty buckets unless you tick *force* (deletes objects and pending uploads first).

## Objects — `#/buckets/<name>`

- Folder-style navigation: prefixes ending in `/` are shown as folders, with a breadcrumb to go back.
- **Filter** by prefix, **Load more** for large buckets.
- **Upload**: pick files or drag & drop them onto the list; they go to the current folder, with per-file progress.
- **Object panel** (click an object): size, ETag, last modified, parts count, content type, system headers (`Cache-Control`, `Content-Disposition`, …), user metadata (`x-amz-meta-*`), preview for images/text/JSON up to 1 MB, **Download** and **Delete**.
- **Delete folder**: deletes every object under the current prefix.

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
- **Custom rule** form with every field from the [rule reference](admin-api.md#fault-injection).
- Delete one rule or all of them.

## Disabling the console

`S3LAB_CONSOLE_ENABLED=false` turns off the UI **and** the admin API. Only the S3 port stays up.
