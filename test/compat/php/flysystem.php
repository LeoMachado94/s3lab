<?php

require '/work/vendor/autoload.php';

use Aws\S3\S3Client;
use League\Flysystem\AwsS3V3\AwsS3V3Adapter;
use League\Flysystem\Config;
use League\Flysystem\Filesystem;
use League\Flysystem\StorageAttributes;
use League\Flysystem\UnableToReadFile;
use League\Flysystem\Visibility;

function step(string $msg): void
{
    echo "== flysystem: $msg\n";
}

function check(bool $ok, string $msg): void
{
    if (! $ok) {
        fwrite(STDERR, "FAIL: $msg\n");
        exit(1);
    }
}

$bucket = getenv('BUCKET');
$endpoint = getenv('AWS_ENDPOINT_URL');

$client = new S3Client([
    'version' => 'latest',
    'region' => getenv('AWS_REGION'),
    'endpoint' => $endpoint,
    'use_path_style_endpoint' => true,
    'credentials' => [
        'key' => getenv('AWS_ACCESS_KEY_ID'),
        'secret' => getenv('AWS_SECRET_ACCESS_KEY'),
    ],
]);

$adapter = new AwsS3V3Adapter($client, $bucket, 'php');
$fs = new Filesystem($adapter);

step('write and read');
$fs->write('docs/hello.txt', 'hello flysystem');
check($fs->fileExists('docs/hello.txt'), 'file should exist');
check($fs->read('docs/hello.txt') === 'hello flysystem', 'read content differs');
check($fs->fileSize('docs/hello.txt') === 15, 'fileSize differs');
check($fs->mimeType('docs/hello.txt') === 'text/plain', 'mimeType differs: '.$fs->mimeType('docs/hello.txt'));

step('writeStream and readStream');
$stream = fopen('php://temp', 'w+');
fwrite($stream, str_repeat('s', 1024 * 1024));
rewind($stream);
$fs->writeStream('docs/stream.bin', $stream);
$out = $fs->readStream('docs/stream.bin');
check(strlen(stream_get_contents($out)) === 1024 * 1024, 'stream size differs');

step('listContents deep and shallow');
$fs->write('docs/sub/deep.txt', 'd');
$deep = array_map(fn (StorageAttributes $a) => $a->path(), $fs->listContents('docs', true)->toArray());
sort($deep);
foreach (['docs/hello.txt', 'docs/stream.bin', 'docs/sub', 'docs/sub/deep.txt'] as $p) {
    check(in_array($p, $deep, true), "$p missing from deep listing: ".implode(',', $deep));
}
$shallow = array_map(fn (StorageAttributes $a) => $a->path(), $fs->listContents('docs', false)->toArray());
check(! in_array('docs/sub/deep.txt', $shallow, true), 'shallow listing must not include nested files');
check(in_array('docs/sub', $shallow, true), 'shallow listing must include the directory');

step('copy and move');
$fs->copy('docs/hello.txt', 'docs/copy.txt');
check($fs->read('docs/copy.txt') === 'hello flysystem', 'copy content differs');
$fs->move('docs/copy.txt', 'docs/moved.txt');
check(! $fs->fileExists('docs/copy.txt') && $fs->fileExists('docs/moved.txt'), 'move failed');

step('visibility');
$fs->write('docs/public.txt', 'p', [Config::OPTION_VISIBILITY => Visibility::PUBLIC]);
check($fs->visibility('docs/public.txt') === Visibility::PUBLIC, 'expected public');
$fs->setVisibility('docs/public.txt', Visibility::PRIVATE);
check($fs->visibility('docs/public.txt') === Visibility::PRIVATE, 'expected private');

step('temporary URL fetched with file_get_contents');
$cmd = $client->getCommand('GetObject', ['Bucket' => $bucket, 'Key' => 'php/docs/hello.txt']);
$url = (string) $client->createPresignedRequest($cmd, '+5 minutes')->getUri();
check(str_contains($url, 'X-Amz-Signature='), 'presigned url has no signature');
check(file_get_contents($url) === 'hello flysystem', 'presigned GET body differs');

step('read of missing file throws');
try {
    $fs->read('docs/nope.txt');
    check(false, 'expected UnableToReadFile');
} catch (UnableToReadFile) {
}

step('delete and deleteDirectory');
$fs->delete('docs/moved.txt');
check(! $fs->fileExists('docs/moved.txt'), 'delete failed');
$fs->deleteDirectory('docs');
check($fs->listContents('', true)->toArray() === [], 'directory should be empty after deleteDirectory');

echo "flysystem: all checks passed\n";
