# Poikile

Private photos and other assets on S3-compatible storage, served by your own Go app from a tiered in-memory cache.

The painted colonnade of [Kolonnade](https://github.com/Kolonnade). The Stoa Poikile in Athens was a public walkway hung with paintings; this library is where your pictures hang, behind a door only your app opens.

Like [Portiko](https://github.com/Kolonnade/Portiko), the name carries a **K**. It makes the name unique, and the K stands for **key**, as in passkey. Paired with Portiko, the pictures here sit behind a sign-in that opens with a passkey.

> **Status: early.** The packages are here and tested. Nothing is tagged yet, so pin a commit.

## What it does

- **Store** originals and their web and mobile sizes on S3-compatible storage, one bucket per owner, with keys your database chooses. Nothing lists a bucket.
- **Upload** through pre-signed PUTs limited to images and to a size.
- **Process** the original into display sizes with private metadata removed, keeping the original untouched.
- **Serve** every byte through your app after it authorizes the request, from two LRU caches (recent and older) so browsing old photos never evicts new ones.
- **Sign in** with passkeys through an optional Portiko adapter. The core doesn't depend on Portiko.

## What it does not do

Poikile never decides who may see an asset. Your app answers that through a resolver, and Poikile only serves what the resolver allows.

## Packages

- `store` — the storage interface, with `s3`, `memory` and `fs` backends.
- `bucket` — one bucket per owner, created and named for you.
- `upload` — pre-signed PUTs limited by type and size.
- `process` — the pipeline that turns an upload into its stored sizes.
- `process/image` — display sizes with private metadata removed.
- `process/image/imagetest` — test images for the image package.
- `cache` — the two-tier in-memory LRU cache.
- `serve` — an `http.Handler` that authorizes, caches and streams.
- `meter` — storage and transfer counters (planned).
- `assettest` — a conformance suite for `store` backends.

Import path: `github.com/Kolonnade/Poikile`.

Import these; don't copy them. If you need something that isn't exported, open an issue asking for a public API.

## Rules

1. No storage URL ever reaches a client, except an upload target.
2. Authorize before cache: every delivery goes through the `Resolver`.
3. Every refusal is the same NotFound.
4. Nothing lists a bucket; the provider interface has no List.
5. Keys are server-chosen UUIDs; filenames never reach storage.
6. Re-encode everything; never copy input bytes through.
7. Never cache errors or NotFound.
8. Byte budgets, not counts.
9. Deletion evicts from every cache tier first.

## Tests

`go test ./...` runs everything that needs no network. The S3 conformance test runs only when `TEST_S3_PROFILE` names an AWS profile; `TEST_S3_REGION` (default `us-east-1`) and `TEST_S3_BUCKET_PREFIX` (default `poikile-test-`) set where it creates its temporary bucket.

HEIF decoding needs libheif and the `heif` build tag: `go test -tags heif ./...`.

## License

MIT. See [LICENSE](LICENSE).
