# Security Policy

## Supported versions

Security fixes are applied to the latest version on the `main` branch.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting feature for this repository. Do not open a public issue containing exploit details, credentials, transcripts, account information, or private filesystem paths.

If private vulnerability reporting is unavailable, open a minimal public issue asking the maintainers for a private contact channel. Do not include sensitive technical details in that issue.

## Sensitive data

Herdr Tandem must never log or expose provider OAuth tokens, refresh tokens, service credentials, Keychain contents, complete environment dumps, task text, final answers, or full private agent transcripts. Herdr Tandem does not read, store, import, refresh, or rotate provider credentials; the external provider service owns authentication and account fallback.

Herdr Tandem keeps only private runtime records and interrupted-task journals in its state directory. They contain pane and session identifiers, task hashes, transcript offsets, timestamps, and delivery receipts—never provider credentials, service secrets, task plaintext, or transcript content.
