# Security Rules

- Never expose a raw error message for a 500. Return `"Internal server error"`.
- Set all secrets through environment variables. Never hard-code credentials.
- GORM parameterizes queries. Never interpolate user input into raw SQL.
- CORS allows `*` in dev only (code default; a startup warning when `GO_ENV=production`). The Helm chart default is `same-origin`: no cross-origin requests, the web UI on the same host passes. Set a list of origins only for other browser clients.
- Rate limiting: a per-IP sliding window on API routes, and a stricter login limiter (`LOGIN_RATE_LIMIT`). The IP is the TCP peer, or the client behind a proxy of `TRUSTED_PROXIES`. Never trust all proxies: a client could then pick a new IP for each request.
- The `SecurityHeaders` middleware sets baseline headers on every response. Never remove it.
- The Recovery middleware catches panics globally. Never remove it.
- `sessionstore` holds the revoked access-token `jti` values, the revoked sessions (`sid`) and the blocked users. `RefreshTokenRepository` holds the revoked refresh tokens. JWT auth and the `/ws` upgrade check the blocklist. Every auth path (login, refresh, OIDC, API key) rejects `User.Disabled`.
- `RedactWSToken` removes `?token=` from `/ws` URLs and from the stream routes before logging, metrics and tracing. Keep it first after RequestID.
- Outbound hooks carry an HMAC signature (`X-StackManager-Signature`) when a subscription has a secret. A subscription without a secret is sent unsigned. Never log hook secrets or provider tokens.
- Kubeconfig data, registry passwords, notification channel secrets and notification channel webhook URLs are encrypted at rest with AES-GCM (`KUBECONFIG_ENCRYPTION_KEY`). Without the key, the backend refuses `kubeconfig_data`, `registry_password` and a non-empty channel `secret` (400). It stores a channel `webhook_url` in plaintext, because the URL is required and the Helm default key is empty.
- Port-forward bypasses NetworkPolicies and the ingress auth of tools. Keep `allow_port_forward` off on clusters with real data. Stream routes take header auth only and refuse API keys unless `STACK_STREAM_ALLOW_API_KEYS=true`. Rules: `docs/claude/domain.md` (Port-forward).
- Exec shows the container environment and its secrets. The pod comes only from a list of the release pods, never from the request. Never wrap the command in a shell. Build the executors only from the cluster `rest.Config` with the stock client-go constructors (a hand-built upgrade transport without a TLS config skips the certificate check). Rules: `docs/claude/domain.md` (Exec).
- Helm runs with the backend identity. On shared clusters: keep `ALLOWED_CHART_REPOS` set, keep `backend.rbac.scope: namespace` and its admission policy, and never give a role `user` path a way to change a chart source.
