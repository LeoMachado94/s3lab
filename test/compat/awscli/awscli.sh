#!/usr/bin/env bash
set -euo pipefail

step() { printf '\n== aws-cli: %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

work=$(mktemp -d)
cd "$work"

step "mb creates a new bucket"
aws s3 mb s3://cli-extra
[[ $(aws s3 ls) == *cli-extra* ]] || fail "cli-extra not listed"

step "cp single small file up and down"
echo "hello s3lab" > hello.txt
aws s3 cp hello.txt "s3://$BUCKET/cli/hello.txt"
aws s3 cp "s3://$BUCKET/cli/hello.txt" hello.down
[ "$(cat hello.down)" = "hello s3lab" ] || fail "small file roundtrip differs"

step "cp 20MiB file (multipart with default chunking)"
dd if=/dev/urandom of=big.bin bs=1M count=20 status=none
aws s3 cp big.bin "s3://$BUCKET/cli/big.bin"
aws s3 cp "s3://$BUCKET/cli/big.bin" big.down
[ "$(sha256sum < big.bin)" = "$(sha256sum < big.down)" ] || fail "multipart roundtrip differs"
size=$(aws s3api head-object --bucket "$BUCKET" --key cli/big.bin --query ContentLength --output text)
[ "$size" = 20971520 ] || fail "head-object size $size != 20971520"

step "sync a directory up and back down"
mkdir -p tree/sub
echo a > tree/a.txt
echo b > tree/sub/b.txt
aws s3 sync tree "s3://$BUCKET/tree"
mkdir back
aws s3 sync "s3://$BUCKET/tree" back
[ "$(cat back/sub/b.txt)" = b ] || fail "sync download differs"

step "ls --recursive lists every key"
listing=$(aws s3 ls "s3://$BUCKET/" --recursive)
for k in cli/hello.txt cli/big.bin tree/a.txt tree/sub/b.txt; do
  [[ $listing == *"$k"* ]] || fail "$k missing from ls"
done

step "ls at root shows common prefixes"
[[ $(aws s3 ls "s3://$BUCKET/") == *"PRE tree/"* ]] || fail "PRE tree/ missing"

step "presign + raw HTTP GET"
url=$(aws s3 presign "s3://$BUCKET/cli/hello.txt" --expires-in 60)
[[ $url == *X-Amz-Signature=* ]] || fail "presigned url has no signature: $url"
path=${url#http://*/}
exec 3<>"/dev/tcp/${S3_HOST%%:*}/${S3_HOST##*:}"
printf 'GET /%s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n' "$path" "$S3_HOST" >&3
resp=$(cat <&3)
exec 3>&-
status_line=${resp%%$'\r'*}
[[ $status_line == *" 200 "* ]] || fail "presigned GET not 200: $status_line"
[[ $resp == *"hello s3lab"* ]] || fail "presigned GET body differs"

step "mv and rm"
aws s3 mv "s3://$BUCKET/cli/hello.txt" "s3://$BUCKET/cli/moved.txt"
aws s3 rm "s3://$BUCKET/cli/moved.txt"
aws s3 rm "s3://$BUCKET/tree" --recursive
if aws s3api head-object --bucket "$BUCKET" --key cli/moved.txt 2>/dev/null; then
  fail "moved.txt still exists after rm"
fi

step "rb --force removes bucket with content"
aws s3 cp hello.txt s3://cli-extra/x.txt
aws s3 rb s3://cli-extra --force
[[ $(aws s3 ls) != *cli-extra* ]] || fail "cli-extra still listed"

echo
echo "aws-cli: all checks passed"
