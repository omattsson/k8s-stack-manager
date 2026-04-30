# Roadmap

The core platform is functional. This tracks what's left, sourced from [GitHub issues](../../issues).

## Security & Operations

- **External Secrets Operator** — migrate Helm values secrets out of the database ([#141](../../issues/141), critical, large)
- **Container image scanning** in CI pipeline ([#140](../../issues/140), high, small)
- **Backup and restore** procedures for MySQL ([#139](../../issues/139), high, medium)
- **Prometheus /metrics endpoint** with application-level metrics ([#136](../../issues/136), medium, small)
- **HPA** in the Helm chart for the stack-manager itself ([#138](../../issues/138), medium, small)

## Deploy Pipeline

- **Gate deploy-finalized on pod readiness**, not just Helm apply ([#186](../../issues/186))
- **Reject deploy when namespace is terminating** ([#182](../../issues/182))
- **SessionStore abstraction** for token blocklist and OIDC state, Redis-ready ([#148](../../issues/148))

## Notifications

In-app notification types not yet wired up:

- Stack TTL expiring ([#189](../../issues/189))
- Cluster quota warning ([#190](../../issues/190))
- ACR pull secret / TLS cert expiring ([#191](../../issues/191))
- Cleanup policy executed ([#192](../../issues/192))
- Deploy timeout for stuck Helm operations ([#193](../../issues/193))

## Webhook Routing

- **Multiple Teams channels** with per-event routing ([#185](../../issues/185))

## Features

- **GitOps mode** — auto-deploy on Git branch push ([#119](../../issues/119), medium, large)
- **GitHub as Git provider** ([#122](../../issues/122), medium, small)
- **Team/project grouping** for instances and templates ([#120](../../issues/120), medium, medium)
- **Dashboard overview widgets** — cluster health, recent deploys, expiring TTLs ([#116](../../issues/116), medium, medium)
- **Cost estimation** per stack instance ([#118](../../issues/118), low, small)

## UX

- **Global search** across instances, templates, definitions ([#129](../../issues/129), medium, medium)
- **Prominent favorites and recent** on dashboard ([#130](../../issues/130), medium, small)
- **Inline batch actions** — checkbox select + bulk ops ([#125](../../issues/125), medium, small)
- **Guided first-run wizard** ([#123](../../issues/123), medium, medium)
- **Dark mode polish** — audit all pages ([#127](../../issues/127), low, small)
- **Mobile responsive layout** ([#128](../../issues/128), low, medium)
- **Keyboard shortcuts and command palette** ([#124](../../issues/124), low, medium)

## Testing

- **Playwright E2E in CI** ([#133](../../issues/133), medium, medium)
- **80% test coverage threshold** in CI ([#132](../../issues/132), medium, small)
