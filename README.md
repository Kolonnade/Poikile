# Poikile

Private photos and other assets on S3-compatible storage, served by your own Go app from a tiered in-memory cache.

The painted colonnade of [Kolonnade](https://github.com/Kolonnade), spelled with a **K** like [Portiko](https://github.com/Kolonnade/Portiko). The Stoa Poikile in Athens was a public walkway hung with paintings; this library is where your pictures hang, behind a door only your app opens.

> **Status: not started.** The packages are being moved here from a private prototype. Nothing is tagged.

## What it will do

- **Store** originals and their web and mobile sizes on S3-compatible storage, one bucket per owner, with keys your database chooses. Nothing lists a bucket.
- **Upload** through pre-signed PUTs limited to images and to a size.
- **Process** the original into display sizes with private metadata removed, keeping the original untouched.
- **Serve** every byte through your app after it authorizes the request, from two LRU caches (recent and older) so browsing old photos never evicts new ones.
- **Sign in** with passkeys through an optional Portiko adapter. The core doesn't depend on Portiko.

## What it will not do

Poikile never decides who may see an asset. Your app answers that through a resolver, and Poikile only serves what the resolver allows.

## License

MIT. See [LICENSE](LICENSE).
