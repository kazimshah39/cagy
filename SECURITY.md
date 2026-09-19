# Security Policy

## Supported versions

Security fixes are applied to the latest version on the `main` branch.

## Reporting a vulnerability

Please use GitHub's private vulnerability reporting feature for this repository. Do not open a public issue containing exploit details, credentials, transcripts, account information, or private filesystem paths.

If private vulnerability reporting is unavailable, open a minimal public issue asking the maintainers for a private contact channel. Do not include sensitive technical details in that issue.

## Sensitive data

cagy must never log or expose OAuth tokens, refresh tokens, API keys, Keychain contents, complete environment dumps, account-backup passphrases, or full private agent transcripts. For this personal-Mac, simplicity-first setup, cagy account snapshots are stored as private `0600` files inside cagy's `0700` state directory so normal startup never requires Keychain approval. agy continues to own its canonical `gemini` Keychain session, and cagy never rewrites that item's access permissions. Account catalog metadata, diagnostics, and encrypted exports must never expose tokens. Do not use this convenience design on a shared or high-security Mac.
