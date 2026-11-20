# Poikile

Private photos and other files on S3-compatible storage, served by your own Go app from a tiered in-memory cache.

The painted colonnade of [Kolonnade](https://github.com/Kolonnade). The Stoa Poikile in Athens was a public walkway hung with paintings; this library is where your pictures hang, behind a door only your app opens.

Like [Portiko](https://github.com/Kolonnade/Portiko), the name carries a **K**. It makes the name unique, and the K stands for **key**, as in passkey. Paired with Portiko, the pictures here sit behind a sign-in that opens with a passkey.

> **Status: early.** The packages are here and tested. Nothing is tagged yet, so pin a commit.

## What it does

- **Store** originals and their web and mobile sizes on S3-compatible storage, one bucket per owner, with keys your database chooses. Nothing lists a bucket.
- **Upload** through pre-signed uploads limited to a type and a size: any image, or the one type the app declared for a file.
- **Process** the original into display sizes with private metadata removed, including a full-size original re-encoded without its metadata.
- **Keep files** (logs, diffs, JSON, PDFs, archives) as uploaded, after checking their size and sniffing their first bytes, and serve them only as downloads.
- **Serve** every byte through your app after it authorizes the request, from two LRU caches (recent and older) so browsing old photos never evicts new ones.
- **Sign in** with passkeys through an optional Portiko adapter (planned). The core doesn't depend on Portiko.

## What it does not do

Poikile never decides who may see an asset. Your app answers that through a resolver, and Poikile only serves what the resolver allows.

## Packages

- `store` — the storage interface, with `s3`, `memory` and `fs` backends.
- `bucket` — one bucket per owner, created and named for you.
- `upload` — pre-signed uploads limited by type and size.
- `process` — the pipeline that turns an upload into its stored sizes, and `StoreFile` for files.
- `process/image` — display sizes with private metadata removed.
- `process/image/imagetest` — test images for the image package.
- `cache` — the two-tier in-memory LRU cache.
- `serve` — an `http.Handler` that authorizes, caches and streams.
- `meter` — storage and transfer counters (planned).
- `assettest` — a conformance suite for `store` backends.

Import path: `github.com/Kolonnade/Poikile`.

Import these; don't copy them. If you need something that isn't exported, open an issue asking for a public API.

## Files

A file is any upload that isn't re-encoded. The app signs its upload for the exact type it declared (`upload.Limits{ContentType: "application/pdf", MaxBytes: n}`) and then calls `process.StoreFile`, which checks the size, sniffs the first 512 bytes and copies the bytes unchanged to `files/<assetID>/file`. It returns the declared and the sniffed type, so the app can record both and refuse the mismatches it cares about; it refuses a declared raster image whose bytes are not an image itself.

`serve` sends a file with its stored type, `Content-Disposition: attachment` and the filename the app's resolver supplies, `X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`. Only raster images (JPEG, PNG, GIF, WebP, AVIF) are ever inline; SVG and HTML are always attachments. Files larger than `StreamAbove` stream from storage instead of filling the cache.

To keep an image's exact bytes as well as its display sizes, call `StoreFile` with `KeepIncoming` and then run the image pipeline on the same upload.

S3 uploads are pre-signed POSTs, not PUTs: only a POST policy can bound the size. The `fs` provider signs PUTs. Send what `upload.Target.Method` says.

## Deleting a bucket

`DeleteBucket` removes an empty bucket, for when its owner goes (a room, say). Nothing lists a bucket, so the app empties it first: it deletes every key its database knows, including the `incoming/` keys of uploads it signed, each with `serve.Delete`, which evicts it from the cache first (rule 9), and then calls `DeleteBucket`. A bucket that still holds anything returns `store.ErrBucketNotEmpty`. A missing bucket is not an error, so a deletion job can stop and resume.

Every bucket is versioned, so on S3 `Delete` removes a key for good: it asks `HeadObject` for the current version or delete marker and deletes it by id until the key is gone, without listing. Versioning still keeps what an overwrite replaces, until `Delete` or `NoncurrentDays`.

## IAM

The role that runs `EnsureBucket` needs `s3:CreateBucket`, `s3:PutBucketPublicAccessBlock`, `s3:PutEncryptionConfiguration`, `s3:PutBucketVersioning`, `s3:PutLifecycleConfiguration`, `s3:PutBucketCORS` and, with a `LogBucket`, `s3:PutBucketLogging`. `DeleteBucket` needs `s3:DeleteBucket`.

The runtime role needs `s3:PutObject` and `s3:GetObject` (which `HeadObject` uses too), and `s3:DeleteObject` and `s3:DeleteObjectVersion` for `Delete`. It never gets `s3:ListBucket` (rule 4).

## Rules

1. No storage URL ever reaches a client, except an upload target.
2. Authorize before cache: every delivery goes through the `Resolver`.
3. Every refusal is the same NotFound.
4. Nothing lists a bucket; the provider interface has no List.
5. Keys are server-chosen UUIDs; filenames never reach storage.
6. Re-encode everything that is displayed inline. Files are stored as uploaded and only ever served as attachments.
7. Never cache errors or NotFound.
8. Byte budgets, not counts.
9. Deletion evicts from every cache tier first.

Rule 6 used to say "re-encode everything". Re-encoding is what makes inline display safe: the browser renders bytes Poikile wrote, with the metadata gone. Logs, archives and PDFs can't be re-encoded, so they are kept as uploaded, and what keeps them safe instead is that a browser never renders them: an attachment, with nosniff and a sandbox, is saved, not shown.

## Tests

`go test ./...` runs everything that needs no network. The S3 conformance test runs only when `TEST_S3_PROFILE` names an AWS profile, or when `TEST_S3_ENDPOINT`, `TEST_S3_ACCESS_KEY` and `TEST_S3_SECRET_KEY` name an S3-compatible server such as a local MinIO; `TEST_S3_REGION` (default `us-east-1`) and `TEST_S3_BUCKET_PREFIX` (default `poikile-test-`) set where it creates its temporary bucket.

```sh
TEST_S3_ENDPOINT=http://127.0.0.1:9000 TEST_S3_ACCESS_KEY=... TEST_S3_SECRET_KEY=... go test ./store/s3/
```

Against an endpoint the test builds its provider with `s3.FromStatic(endpoint, accessKey, secretKey, s3.Options{AllowUnsupported: true})`, as a local dev setup should: MinIO doesn't implement the public access block, CORS, access logging, or encryption without a KMS, so `EnsureBucket` skips those steps there. Versioning and lifecycle are always applied. Leave `AllowUnsupported` off on AWS.

HEIF decoding needs libheif and the `heif` build tag: `go test -tags heif ./...`. The HEIC test reads `process/image/testdata/sample.heic` and skips without it; make one with `heif-enc`.

## License

MIT. See [LICENSE](LICENSE).
