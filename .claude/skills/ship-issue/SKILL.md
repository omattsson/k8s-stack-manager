---
name: ship-issue
description: Plan a GitHub issue or feature in this repository and carry it out by starting the specialist agents (go-api-developer, frontend-developer, git-provider, helm-values, devops-engineer, qa-engineer, code-reviewer) from the main session. Use for multi-step work across backend, frontend, tests, and infrastructure.
argument-hint: "<issue number or feature description>"
---

# Ship an issue

You are the tech lead in the main session. Plan the work, start the specialist agents directly, and track the result. This replaces the former `orchestrator` subagent: a planning subagent repeated the work of the main session, and the results of every agent that it started stayed in its context.

## Plan

1. Read the issue: `gh issue view <number>`. `CLAUDE.md` is loaded already. Read a `docs/claude/*.md` file only for the area that the issue touches.
2. Choose a workflow sequence below. Leave out steps that the change does not need.
3. Small change (one package, a few files, no new route or migration): do it in the main session, without agents. An agent costs a fresh context and a summary round trip.
4. Write a short numbered plan: one agent for each step, with acceptance criteria. Show it to the user before the first agent starts when the scope is not clear.

## Delegate

Give each agent a self-contained brief. The agent does not see this conversation.

- The goal, the acceptance criteria, and the issue number.
- The files and functions to start from, and the facts that you already found. The agent should not search for them again.
- The branch or worktree to work in.
- Ask for a short report: files changed, tests run with their results, and open questions. No full diffs and no full command output.

Start independent steps in parallel (for example backend and Helm values). Start dependent steps in order.

## Team

| Agent | Use for |
| --- | --- |
| go-api-developer | Models, repositories, migrations, handlers, routes, auth, hooks, Swagger, backend tests |
| frontend-developer | Pages, components, API services, routing, WebSocket, frontend tests |
| git-provider | Azure DevOps, GitHub and GitLab: branch listing, URL parsing and detection, provider authentication |
| helm-values | Values merge, template variables, Helm CLI wrapper |
| devops-engineer | Docker, nginx, Helm chart, Makefile, CI/CD |
| qa-engineer | Test strategy, coverage gaps, integration and end-to-end tests |
| code-reviewer | Review of finished work, security review |

## Implementation order (full stack)

1. Models and repositories.
2. Handlers and routes.
3. API client (`frontend/src/api/client.ts`).
4. UI pages.
5. Tests.

## Workflow sequences

### New API resource (full stack)

1. go-api-developer: model, validation, migration, repository, handler, routes, audit route entry, Swagger, backend tests.
2. frontend-developer: API service, page, routing, navigation, frontend tests.
3. qa-engineer: coverage gaps across both, and the end-to-end spec.
4. code-reviewer: full review.

### Auth or OpenID Connect (OIDC) change

1. go-api-developer: auth handler or middleware, session store, OIDC provider.
2. qa-engineer: all auth paths (login, refresh, OIDC callback, API key, disabled user).
3. frontend-developer: Login page, AuthCallback, AuthContext.
4. code-reviewer: security-focused review.

### Hooks or actions

1. go-api-developer: dispatcher, HMAC signing, action routes. Update `backend/docs/hooks.md` and `EXTENDING.md`.
2. devops-engineer: Helm values for hooks (`hooks.subscriptions`, `hooks.actions`).
3. qa-engineer: tests for the hook lifecycle.
4. code-reviewer: security review (HMAC, timeouts, redirects, `failure_policy`).

### Chart source, RBAC, or namespace change

1. go-api-developer: `ALLOWED_CHART_REPOS` checks, access rules, `EnsureNamespace`, quota fields.
2. devops-engineer: chart templates and values (`backend.rbac.*`, `backend.allowedChartRepos`). Test with the Helm version of the backend image (`HELM_VERSION` in `backend/Dockerfile`).
3. qa-engineer: role tests (`user`, `devops`, `admin`) and `make helm-test`.
4. code-reviewer: security review.

### Bug fix

1. Write a test that fails because of the bug (qa-engineer, or in the main session for a small fix).
2. go-api-developer or frontend-developer: fix the bug, unless the main session already can.
3. code-reviewer: review the fix.

### Backend-only or frontend-only change

1. The matching developer agent builds the change, with tests.
2. code-reviewer: review. Add qa-engineer only when the review finds coverage gaps.

## Finish

1. Check the reports against the acceptance criteria. Run the tests of the changed packages yourself before you report done.
2. Commit on the branch. Push and open a pull request only when the user asks.
3. When the context of this session is past about 40%, hand over before the next large step: run `/handoff` if you have that skill, or `/compact` with a focus on the remaining work.
