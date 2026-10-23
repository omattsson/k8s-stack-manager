# Hooks and Actions

Reference for Claude Code and other agents. The root `CLAUDE.md` links here. Read this file before you work in this area. The contract is in `backend/docs/hooks.md` and `EXTENDING.md`.

**Hooks.**
- `hooks.Dispatcher.Fire` sends HTTP webhooks for the lifecycle events that the core fires:
  - Deploy: `pre-deploy`, `post-deploy`, `deploy-finalized`, `deploy-timeout`.
  - Rollback: `pre-rollback`, `post-rollback`, `rollback-completed`.
  - Instance create and delete: `pre-instance-create`, `post-instance-create`, `pre-instance-delete`, `post-instance-delete` (single, bulk, after-clean and cleanup policy deletes).
  - Completion: `stop-completed`, `clean-completed` (`metadata.operation=delete` when `delete-completed` follows), `delete-completed`.
  - Cleanup policy: `cleanup-policy-executed` (once per policy run with a match; the summary is in `cleanup_policy`).
- The config accepts the constants `instance-created`, `stack-expiring`, `stack-expired`, `quota-warning` and `secret-expiring`, but the core does not fire them. `pre-namespace-create` and `post-namespace-create` are reserved.
- Signature: when a subscription sets `secret_env`, the request has `X-StackManager-Signature`. A `secret_env` that resolves to an empty value is a config-load error. Omit `secret_env` for unsigned dispatch.
- Envelopes carry `trigger`: `user` (ID and username), `cleanup-policy` (policy ID and name) or `ttl`. The caller sets it with `hooks.WithTrigger`. The deploy manager keeps it for each deploy log.
- A `post-deploy` subscription with `blocking: true` runs after the releases are ready. The instance stays `stabilizing`.
  - `LOG:` lines go into the deploy log. The time limit is the sum of the blocking timeouts plus 1 minute, separate from the Helm budget. The wait holds no concurrency slot.
  - Only a stop ends the wait early. `stack_instances.post_deploy_hook_until` makes the status watcher of the leader keep the status.
  - A shutdown sets `error` with "Interrupted by a server restart. Deploy again." After a hard kill, the interrupted operation recovery sets `error` (see `docs/claude/operations.md`).
  - After the wait: status `running`, notification "Deployment succeeded", then `deploy-finalized`. A failure gives `error` (`failure_policy: fail`) or a `deployment.warning` notification (`ignore`).
- A `pre-*` subscriber with `failure_policy: fail` can abort the operation.
  - `hooks.UserMessage` builds the safe reason (hook name and subscriber message, never the URL).
  - The reason goes into the `error_message` of the instance and of the deploy log, and into an `ERROR:` line of the log output.
- A stop cancels a running `pre-deploy` or `pre-rollback` call. A clean or a new deploy answers 409 while the instance is deploying.
  - The deployer checks the instance at each stabilize poll interval (`DEPLOY_STABILIZE_POLL_INTERVAL`, default 5s), as for blocking `post-deploy` (`deployer/hook_watch.go`). It closes the request when the status is no longer `deploying` or a newer deploy log exists.
  - The log ends with `WARNING: deploy cancelled ...` (or `rollback cancelled ...`). Helm does not run, and the other operation keeps its status. `failure_policy` does not apply. A shutdown stays a hook failure.
  - The status watcher of the leader never sets a `deploying` instance to `error`. The deploy sets the final status.
- Event hooks never follow redirects (`noRedirectClient`, as actions). A 3xx answer is a subscriber failure (`hooks.RedirectError`, the user message has no URL). `failure_policy` applies.
- Subscriptions come from `HOOKS_CONFIG_FILE` (Helm: `hooks.subscriptions`). The contract is in `backend/docs/hooks.md`.

**Actions.**
- `hooks.ActionRegistry` holds the custom actions of `HOOKS_CONFIG_FILE` (Helm: `hooks.actions`).
- `GET /stack-instances/:id/actions` lists them for any user who can view the instance. The list has UI metadata (`label`, `confirm`, `parameters`) and `can_invoke` (the instance modify rule). A parameter has: name, label, type (`string`, `bool` or `enum`), required, default, options, sensitive. The list never returns `url` or `secret_env`.
- Each invoke that reaches the subscriber writes one `action_runs` row (migration 61): user, declared parameters only (a `sensitive` parameter as `[redacted]`), `status_code` (0 = no answer; a 3xx or non-JSON answer keeps its code, the client still gets 502), `message`, `job_id`. The envelope gets `run_id`. A write failure is logged and never fails the action (#640).
- A parameter with `sensitive: true` (type `string` only, no `default`) goes to the subscriber unchanged. The run history stores `[redacted]`, and the dialog shows a password field.
- `GET /stack-instances/:id/action-runs` (`limit` default 20, max 100; `cursor`; newest first) lists the runs for any user who can view the instance, with `started_by`, `action_label`, `has_job_log` and `can_view_logs` (the modify rule). An instance delete removes the runs (`DeleteInstanceRecord`). A user delete keeps them ("Unknown user").
- `POST /stack-instances/:id/actions/:name` checks the declared parameters. Undeclared parameters pass through. When the action has `log_path`, a valid `result.job_id` of a 2xx answer goes into the envelope.
- `GET /stack-instances/:id/actions/:name/jobs/:job_id/log?offset=N` proxies the job log. It needs the modify rule, because a job log can hold sensitive output.
  - `log_path` resolves on the host of the action URL only.
  - `job_id` must match `^[A-Za-z0-9_-][A-Za-z0-9._-]{0,127}$`.
  - The GET is signed (HMAC of the request URI). It carries `instance_id` and `ts` (Unix seconds). Subscribers check a 5-minute window and must answer 404 for a job of another instance.
  - Limits: 10s timeout, 256 KiB (cut at a line end, then `truncated: true`).
  - Redirects are not followed (also on invoke). An upstream failure gives 502 without the URL.
  - The response is JSON `{status, log, offset, next_offset, done, truncated}`.
  - `status` comes from `X-Job-Status` (preferred) or, as a fallback, from a line `===<NAME>-END=== status=<s>`.
  - `X-Log-Offset` counts only when it is not smaller than the offset.
  - A poll that returns `done` stores the final status on the run (`job_status`, `finished_at`) with one conditional update, so the first poll on any replica wins.
- The web UI shows the custom actions as the first group of the instance "Actions" menu (`useInstanceActions` in `components/InstanceActionsMenu`). The menu also holds export values, clone, clean namespace and delete. The instance page lists the runs below the Deployment History (`components/ActionRunHistory`: 10 rows, "Show more", "Show log" for a run with a job when the user may modify the instance).
- The job log dialogs (`useJobLog`, `JobLogPanel`; `JobLogDialog` for a past run polls at once) poll every 3 s. It backs off on 429, 502, 503, 504 and network errors (`Retry-After`, else a doubling delay up to 30 s). It stops on done, on close, on 401, 403 or 404, and after 60 minutes.
