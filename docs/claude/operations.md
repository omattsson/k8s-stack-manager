# Scalability and Operations

Reference for Claude Code and other agents. The root `CLAUDE.md` links here. Read this file before you work in this area.

## Scalability Notes

- MySQL connection pool: `DB_MAX_OPEN_CONNS` (25), `DB_MAX_IDLE_CONNS` (5), `DB_CONN_MAX_LIFETIME` (5m).
- Database retry: 5 attempts, 2s delay at startup.
- Docker networks: `backend-net` (database, backend) and `frontend-net` (backend, frontend). Keep them separate. Their names are fixed (`backend-network`, `frontend-network`), so `make dev` and `make test-e2e` share them (#581). Remove the old networks once with `docker network prune`.
- Health checks: register every external dependency with `healthChecker.AddCheck()`.
- Background workers run on the leader replica only (`internal/leader`, Kubernetes Lease).
  - Add a periodic job as a `leader.Worker` with a restartable `Run(ctx)`. Keep state that must survive a leader change in the database.
  - Readiness does not depend on leadership.
- WebSocket messages and socket revocations reach all replicas through the hub fan-out (`WS_FANOUT_ENABLED`, `ws_events`, 500ms poll).
  - The hub send never blocks. A full buffer drops the row. The `ws_events` writes also go to the MySQL binary log.
  - Send through `Broadcast`, `BroadcastToInstance` or `BroadcastToUser`. Revoke through `DisconnectUser`, `DisconnectToken` or `DisconnectAPIKey`.
- Notification channel workers (#649): a channel worker starts with the first event of the channel and exits after 10 minutes without work. One mutex guards the worker map and the idle exit. `Notifier.Stop` accepts no new event. The queued events go out for at most 2 s. Then `Stop` cancels the rest, counts it as dropped (`shutdown`) and waits for all workers. A cancelled request still writes its delivery log.
- Deploy, rollback, stop and clean run on the replica that took the request.
  - Each deploy log stores the process identity (`deployment_logs.replica_id`).
  - Every replica writes a heartbeat (`replica_heartbeats`, every 30s).
  - The leader worker `interrupted-operation-recovery` runs at the start of a term and then every minute. It sets `error` with the message "Interrupted: the server that ran this operation stopped. Deploy again." (or "Stop again.", "Clean again.") for an operation that is past `deployment_logs.deadline_at` and whose process has no heartbeat for 2 minutes (database time, excluded in SQL).
  - `deadline_at` is the time budget of the operation plus 5 minutes. The backend sets it at the start and moves it when the operation gets its concurrency slot.
  - The update is a compare-and-set on the instance status and `updated_at`. It never ends an operation with a fresh heartbeat. The audit action is `interrupted`.
  - Every final deploy log write is conditional on `status = 'running'` (`models.ErrDeployLogNotRunning`). A late goroutine writes nothing after a recovery.
  - A shutdown does not stop the final writes: they use `Manager.finalWriteCtx` (detached, 30 s limit), so the log and the instance get their final status. A shutdown after the Helm installs gives `error` "Interrupted by a server restart. Deploy again.", never `running`, unless one last status read finds the pods healthy and no blocking `post-deploy` subscriber must run.
  - During an operation, the deploy manager writes the streamed lines to `deployment_logs.output` every 5s (`DeploymentLogOutputWriter.UpdateRunningOutput`, also conditional on `running`, up to `maxOutputLen`). A page reload then shows the output so far. `writeFinal` drops the live output.
  - Gap: `stack_instances` has no version column. A stale writer (status watcher, TTL reaper) can overwrite the recovered error.
- Log retention: the leader worker `log-retention` (`internal/retention`, `RetentionRepository`) deletes old rows of `deployment_logs`, `audit_logs`, `notifications`, `notification_delivery_logs` and `action_runs` (`*_RETENTION_DAYS`, `0` keeps them). It deletes in batches of 1000 IDs (100 for `deployment_logs`) by database time. For each existing instance it keeps the running deploy logs, the newest `deploy` or `rollback` log (any status), the newest successful `deploy` log and the two newest successful `deploy` or `rollback` logs (values drift, rollback branch). A rollback to a deleted target log gives 404. The Analytics deploy counts cover only the kept deploy logs (`deploy_log_retention_days` in the overview). A new log table that grows needs a retention there.
- Always paginate new list endpoints.
  - Use the query parameters `page` and `pageSize` (default 25, maximum 100) with `ListPaged(..., limit, offset)`, as stack instances, definitions and templates do.
  - Stack instances and definitions also accept a `limit` and `offset` fallback. Templates take `page` and `pageSize` only.
  - Older endpoints (items, audit logs, notifications, delivery logs) take `limit` and `offset`. Small admin lists are not paged.
  - Select only the columns that a list view needs (omit TEXT fields like `description`).
  - Give a list query an index that covers its filter and its order. Example: `notifications (user_id, created_at DESC)` (migration 63, #654), so the notification list reads one page of rows, not all rows of the user.
  - Use batch queries (`CountByTemplateIDs`, `FindByIDs`) instead of N+1 loops for enrichment data.
- Use `internal/cache` for short-lived in-memory caching instead of ad-hoc maps. The combined auth, login, dashboard and analytics handlers use it (`LOGIN_CACHE_TTL`). `gitprovider.Registry` keeps its own 5-minute branch cache.
- Struct field order: put 8-byte fields first for memory alignment.
