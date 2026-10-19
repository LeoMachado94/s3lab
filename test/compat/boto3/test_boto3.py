import hashlib
import os
import sys
import tempfile

import boto3
import requests
from boto3.s3.transfer import TransferConfig
from botocore.config import Config
from botocore.exceptions import ClientError

ENDPOINT = os.environ["AWS_ENDPOINT_URL"]
ADMIN = os.environ["S3LAB_ADMIN_URL"]
BUCKET = os.environ["BUCKET"]

s3 = boto3.client(
    "s3",
    endpoint_url=ENDPOINT,
    config=Config(signature_version="s3v4", s3={"addressing_style": "path"}),
)


def step(msg):
    print(f"== boto3: {msg}", flush=True)


def admin(method, path, **kw):
    auth = (os.environ["AWS_ACCESS_KEY_ID"], os.environ["AWS_SECRET_ACCESS_KEY"])
    r = requests.request(method, ADMIN + path, timeout=10, auth=auth, **kw)
    r.raise_for_status()
    return r


def sha(b):
    return hashlib.sha256(b).hexdigest()


def main():
    admin("POST", "/_s3lab/reset", params={"bucket": BUCKET})

    step("put/get/head")
    s3.put_object(Bucket=BUCKET, Key="py/a.txt", Body=b"alpha", ContentType="text/plain", Metadata={"k": "v"})
    got = s3.get_object(Bucket=BUCKET, Key="py/a.txt")
    assert got["Body"].read() == b"alpha"
    assert got["ContentType"] == "text/plain", got["ContentType"]
    head = s3.head_object(Bucket=BUCKET, Key="py/a.txt")
    assert head["ContentLength"] == 5
    assert head["Metadata"] == {"k": "v"}, head["Metadata"]

    step("get missing key raises NoSuchKey")
    try:
        s3.get_object(Bucket=BUCKET, Key="py/missing")
        raise AssertionError("expected NoSuchKey")
    except ClientError as e:
        assert e.response["Error"]["Code"] == "NoSuchKey", e.response["Error"]

    step("list_objects_v2 with prefix, delimiter and pagination")
    for i in range(5):
        s3.put_object(Bucket=BUCKET, Key=f"list/item-{i}", Body=b"x")
    s3.put_object(Bucket=BUCKET, Key="list/sub/deep", Body=b"x")
    r = s3.list_objects_v2(Bucket=BUCKET, Prefix="list/", Delimiter="/")
    assert [c["Key"] for c in r["Contents"]] == [f"list/item-{i}" for i in range(5)], r["Contents"]
    assert [p["Prefix"] for p in r["CommonPrefixes"]] == ["list/sub/"]
    keys = []
    for page in s3.get_paginator("list_objects_v2").paginate(Bucket=BUCKET, Prefix="list/", PaginationConfig={"PageSize": 2}):
        keys += [c["Key"] for c in page.get("Contents", [])]
    assert len(keys) == 6, keys

    step("upload_file 12MiB (multipart) and download_file")
    data = os.urandom(12 * 1024 * 1024)
    with tempfile.TemporaryDirectory() as d:
        src, dst = os.path.join(d, "src"), os.path.join(d, "dst")
        with open(src, "wb") as f:
            f.write(data)
        cfg = TransferConfig(multipart_threshold=5 * 1024 * 1024, multipart_chunksize=5 * 1024 * 1024)
        s3.upload_file(src, BUCKET, "py/big.bin", Config=cfg)
        s3.download_file(BUCKET, "py/big.bin", dst, Config=cfg)
        with open(dst, "rb") as f:
            assert sha(f.read()) == sha(data), "multipart roundtrip differs"
    ops = [e["op"] for e in admin("GET", "/_s3lab/requests", params={"bucket": BUCKET, "key": "py/big.bin"}).json()["entries"]]
    assert "CreateMultipartUpload" in ops and "CompleteMultipartUpload" in ops, ops
    assert ops.count("UploadPart") == 3, ops

    step("copy_object")
    s3.copy_object(Bucket=BUCKET, Key="py/copy.txt", CopySource={"Bucket": BUCKET, "Key": "py/a.txt"})
    assert s3.get_object(Bucket=BUCKET, Key="py/copy.txt")["Body"].read() == b"alpha"

    step("delete_objects")
    res = s3.delete_objects(Bucket=BUCKET, Delete={"Objects": [{"Key": f"list/item-{i}"} for i in range(5)]})
    assert len(res["Deleted"]) == 5 and not res.get("Errors"), res
    r = s3.list_objects_v2(Bucket=BUCKET, Prefix="list/item-")
    assert r["KeyCount"] == 0, r

    step("presigned GET and PUT via requests")
    s3.put_object(Bucket=BUCKET, Key="py/presign.txt", Body=b"presigned")
    url = s3.generate_presigned_url("get_object", Params={"Bucket": BUCKET, "Key": "py/presign.txt"}, ExpiresIn=60)
    r = requests.get(url, timeout=10)
    assert r.status_code == 200 and r.content == b"presigned", (r.status_code, r.text)
    url = s3.generate_presigned_url("put_object", Params={"Bucket": BUCKET, "Key": "py/uploaded.txt"}, ExpiresIn=60)
    r = requests.put(url, data=b"via-put", timeout=10)
    assert r.status_code == 200, (r.status_code, r.text)
    assert s3.get_object(Bucket=BUCKET, Key="py/uploaded.txt")["Body"].read() == b"via-put"
    bad = url.replace("X-Amz-Signature=", "X-Amz-Signature=00")
    assert requests.put(bad, data=b"x", timeout=10).status_code == 403, "tampered signature must be rejected"

    step("fault injection: SlowDown is retried by boto3")
    admin("DELETE", "/_s3lab/faults")
    admin("POST", "/_s3lab/faults", json={"op": "PutObject", "bucket": BUCKET, "status": 503, "code": "SlowDown", "count": 2})
    retrying = boto3.client(
        "s3",
        endpoint_url=ENDPOINT,
        config=Config(signature_version="s3v4", s3={"addressing_style": "path"}, retries={"max_attempts": 5, "mode": "standard"}),
    )
    retrying.put_object(Bucket=BUCKET, Key="py/retry.txt", Body=b"ok")
    puts = admin("GET", "/_s3lab/requests", params={"bucket": BUCKET, "op": "PutObject", "key": "py/retry.txt"}).json()["entries"]
    statuses = [e["status"] for e in puts]
    assert statuses == [503, 503, 200], statuses

    step("fault injection: surfaces error without retries")
    admin("POST", "/_s3lab/faults", json={"op": "GetObject", "bucket": BUCKET, "status": 500, "code": "InternalError", "count": 1})
    noretry = boto3.client(
        "s3",
        endpoint_url=ENDPOINT,
        config=Config(signature_version="s3v4", s3={"addressing_style": "path"}, retries={"total_max_attempts": 1, "mode": "standard"}),
    )
    try:
        noretry.get_object(Bucket=BUCKET, Key="py/retry.txt")
        raise AssertionError("expected InternalError")
    except ClientError as e:
        assert e.response["Error"]["Code"] == "InternalError", e.response["Error"]
    admin("DELETE", "/_s3lab/faults")

    print("boto3: all checks passed")


if __name__ == "__main__":
    try:
        main()
    except AssertionError as e:
        print(f"FAIL: {e}", file=sys.stderr)
        sys.exit(1)
