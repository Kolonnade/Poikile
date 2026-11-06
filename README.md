# Poikile

Private photos and other assets on S3-compatible storage, served by your own Go app from a tiered in-memory cache.

The painted colonnade of [Kolonnade](https://github.com/Kolonnade). The Stoa Poikile in Athens was a public walkway hung with paintings; this library is where your pictures hang, behind a door only your app opens.

Like [Portiko](https://github.com/Kolonnade/Portiko), the name carries a **K**. It makes the name unique, and the K stands for **key**, as in passkey. Paired with Portiko, the pictures here sit behind a sign-in that opens with a passkey.

> **Status: not started.** The packages are being moved here from a private prototype. Nothing is tagged.

## What it will do

- **Store** originals and their web and mobile sizes on S3-compatible storage, one bucket per owner, with keys your database chooses. Nothing lists a bucket.
- **Upload** through pre-signed PUTs limited to images and to a size.
- **Process** the original into display sizes with private metadata removed, keeping the original untouched.
- **Serve** every byte through your app after it authorizes the request, from two LRU caches (recent and older) so browsing old photos never evicts new ones.
- **Sign in** with passkeys through an optional Portiko adapter. The core doesn't depend on Portiko.

## What it will not do

Poikile never decides who may see an asset. Your app answers that through a resolver, and Poikile only serves what the resolver allows.

## Packages

- `store` — the storage interface, with `s3`, `memory` and `fs` backends.
- `bucket` — one bucket per owner, created and named for you.
- `upload` — pre-signed PUTs limited by type and size.
- `process/image` — display sizes with private metadata removed.
- `cache` — the two-tier in-memory LRU cache.
- `serve` — an `http.Handler` that authorizes, caches and streams.
- `meter` — storage and transfer counters.
- `assettest` — a conformance suite for `store` backends.

Import path: not final yet.

Import these; don't copy them. If you need something that isn't exported, open an issue asking for a public API.

## License

MIT. See [LICENSE](LICENSE).
