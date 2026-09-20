# Security Policy

## Supported versions

Security fixes are applied to the latest version on the `main` branch.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting feature for this repository. Do not open a public issue containing exploit details, credentials, transcripts, account information, or private filesystem paths.

If private vulnerability reporting is unavailable, open a minimal public issue asking the maintainers for a private contact channel. Do not include sensitive technical details in that issue.

## Sensitive data

cagy must never log or expose provider OAuth tokens, refresh tokens, 9Router API keys or CLI tokens, Keychain contents, complete environment dumps, task text, final answers, or full private agent transcripts. cagy does not read, store, import, refresh, or rotate provider credentials; 9Router owns provider authentication and account fallback.

Cagy keeps only private runtime records and interrupted-task journals in its state directory. They contain pane/session identifiers, task hashes, transcript offsets, timestamps, and delivery receipts—never provider credentials, 9Router secrets, task plaintext, or transcript content.
