# Security

## Rules

1. Never log raw API keys, storage secrets, private keys, or passwords.
2. Never return credentials in API responses.
3. Use scoped API keys for privileged endpoints.
4. Validate all paths and prevent path traversal.
5. Construct process arguments without shell interpolation.
6. Treat media inputs as untrusted.
7. Enforce upload destination boundaries.
8. Support TLS for remote API exposure.
9. Sign webhooks with HMAC and include replay protection guidance.
10. Use least-privilege filesystem permissions for staging and output.

## Worker Security

Distributed workers must authenticate to the master. Worker tokens should be rotatable and scoped to worker registration, heartbeat, assignment polling, progress reporting, and completion reporting.

## Secret Configuration

Secrets may come from environment variables, config files with restricted permissions, or secret managers. Do not hardcode production credentials.
