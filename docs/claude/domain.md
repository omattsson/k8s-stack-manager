# Domain Conventions

Reference for Claude Code and other agents. The root `CLAUDE.md` links here. Read this file before you work in this area.

## Domain: K8s Stack Manager

Developers use this application to configure, store and deploy multi-service Helm-based application stacks to shared Kubernetes clusters.

### Domain Conventions


**Instances**
- **Branch default:** `master` (`DEFAULT_BRANCH`), unless the stack definition sets `DefaultBranch`.
- **Namespace naming:** the handler generates `stack-{instance-name}-{owner}` (`buildNamespace`).
  - When that name has more than 63 characters, the namespace is `stack-{cut instance-name}-{hash6}-{owner}`. `hash6` is the first 6 hex characters of the SHA-256 of the full sanitized instance name and owner, so names that differ after the cut give different namespaces.
  - The owner is cut only when the instance part keeps fewer than 10 characters. A name that fits does not change. A rename keeps the namespace.
- **Instance names:** a new or changed name must be an RFC 1123 label (lowercase `a-z`, `0-9`, `-`; start and end alphanumeric; at most 50 characters). `models.ValidateInstanceName` checks it on create, rename, clone and quick deploy. `StackInstance.Validate` does not check it, so older instances keep working.
- **Unique names for each cluster:** with `UNIQUE_INSTANCE_NAMES=true`, a name must be unique on its cluster. Turn it on when Ingress hosts use `{{.InstanceName}}`: two stacks with the same name claim the same host.
  - Create, rename, clone and quick deploy refuse a name that another instance uses on the same cluster: 409 `instance name already exists`, message `A stack named "x" already exists on this cluster (owner: y)`, and free name suggestions.
  - The comparison ignores case. An instance without a cluster counts on the default cluster. A rename does not count the instance itself.
  - Generated clone names skip the names taken on the cluster. When all are taken, the 409 error is `no free clone name`.
  - The suggestions (in this 409 and in the namespace 409) are names that are free on the cluster and give a free namespace for the caller.
  - The create form, the clone dialog and the quick deploy dialog show the message and the free names (`describeNameConflict` in `utils/apiError.ts`).
  - `instanceNameGuard` (`handlers/instance_names.go`) runs the check through `models.InstanceNameLookup`. It is an application check after the namespace check: two concurrent requests can both pass.
  - Existing duplicates stay. At startup the backend logs a warning for each duplicate name (`LogDuplicateInstanceNames`).
- **Clone:** `POST /stack-instances/:id/clone` takes an optional `{name, branch, ttl_minutes}` or `expires_at` instead of `ttl_minutes`.
  - Without a name, it picks `<name>-copy`, `<name>-copy-2`, and so on.
  - It copies the cluster, TTL (or a future fixed stop time), value overrides and branch overrides.
  - It copies the quota override only when the caller may modify the source (owner, admin, devops). An owner without the admin or devops role gets the copy only when the override stays within the cluster quota. Otherwise the clone uses the cluster quota.
- **Per-chart branch overrides:** an instance can override the branch of each chart (default: `DefaultBranch` of the definition). Helm values use it through `{{.Branch}}` and `{{.ImageTag}}`.
- **Instance comparison:** `GET /stack-instances/compare` compares two instances side by side, including the merged Helm values of each chart.
- **List filters and names:**
  - `GET /stack-instances` filters by `name`, `status` (unknown value: 400), `cluster_id`, `definition_id` and `owner` (`me`, a username or a user ID). The filters combine with AND. `total` counts the matches.
  - `GET /stack-definitions` filters by `name` and `owner` the same way.
  - Instance responses add `owner_username`, `definition_name` and `cluster_name`. Definition and template responses add `owner_username`.
  - Each response uses one batch lookup for each kind. A missing owner, definition or cluster omits the field.
- **Primary URL:** an Ingress with the annotation `k8s-stack-manager.io/primary-url: "true"` (`k8s.PrimaryURLAnnotation`) is the main entry point of the stack. `GET /stack-instances/:id/status` lists it first (`primary: true`), and the stack list card shows it. Without the annotation, the first Ingress by name is the primary URL.
- **TTL auto-expiry:**
  - A create request without `ttl_minutes` gets `DEFAULT_INSTANCE_TTL_MINUTES` (Helm chart default `480`, code default `0` = no expiry). `GET /ui-config` returns it as `default_instance_ttl_minutes` (0 when it is out of range). Create Instance and Quick Deploy start the TTL field with it and leave out `ttl_minutes` until the user changes the field.
  - `MAX_INSTANCE_TTL_DAYS` (default 30, 1 to 365; an invalid value, or a `DEFAULT_INSTANCE_TTL_MINUTES` above it, stops the start and fails the Helm render; Helm `backend.maxInstanceTtlDays`) limits the TTL and the stop time in create, update, clone, quick deploy and extend (400). A deploy caps a TTL expiry at now plus the limit. `GET /ui-config` returns it as `max_instance_ttl_days`. `handlers/instance_ttl.go` holds the rules (`requestedExpiry`, `stopTimeError`).
  - Fixed stop time: create, update, clone and quick deploy accept `expires_at` (RFC 3339 with a time zone) instead of `ttl_minutes`. It sets `ExpiresAt` and `TTLMinutes = 0` (no new column). Both in one request, a past time or a time after the limit give 400. On update, an `expires_at` equal to the stored value is no change, and with an unchanged `ttl_minutes` the expiry stays (clients that send the whole instance back keep a TTL expiry or a fixed stop time). A clone copies a future fixed stop time of the source.
  - An instance with `TTLMinutes > 0` gets `ExpiresAt` on deploy: now plus the TTL, or the current expiry when that is later. A redeploy never shortens an extended expiry. Bulk deploy does the same. A fixed stop time stays on deploy; when it has passed, deploy and bulk deploy refuse (409 "The stop time has passed. Set a new stop time or a TTL.").
  - The reaper (leader replica only) checks every minute and stops expired instances.
  - The expiry warner sends one warning 30 minutes before expiry. It sets `stack_instances.expiry_warned_at` with a conditional update before it notifies. The repository `Update` never writes that column. It clears the column when `ExpiresAt` changes (deploy, extend).
  - `POST /:id/extend` with `{"minutes": N}` adds N minutes to the expiry. It never shortens the expiry, never changes `ttl_minutes`, and caps the result at now plus `MAX_INSTANCE_TTL_DAYS`. `{"until": "<RFC 3339>"}` sets the expiry to that time: after the current expiry, at most now plus the limit (400), for a TTL and a fixed stop time. An empty body adds one TTL. The deprecated body `{"ttl_minutes": N}` alone keeps the old reset behavior.
  - Web UI: "Custom stop time" in `TtlSelector` shows `StopTimePicker` (native `datetime-local`, now plus 15 minutes to now plus `max_instance_ttl_days`, local time zone) and the forms send `expires_at`. The Extend menu has "Custom..." (a dialog that sends `until`). `ExpiryChip` and the instance page show a fixed stop time as "Stops at <time>", also before the deploy.
- **Follow:**
  - Any user who can view an instance can follow it: `POST` and `DELETE /stack-instances/:id/follow` (idempotent, audited as `follow` and `unfollow`).
  - `GET /stack-instances/:id` adds `following` and `follower_count`.
  - Table `instance_followers` (primary key `user_id, instance_id`, index on `instance_id`).
  - Every instance delete path removes the followers and the action runs (`DeleteInstanceRecord`). A user delete removes the follows of the user.
  - Followers get in-app notifications only.
- **Quick deploy:** one-click flow from a template to a new instance and a deploy. It generates the instance name and the namespace.
  - The definition that it creates has `OwnerInstanceID` set.
  - Deleting the instance deletes that definition when no other instance uses it. This also works when the owner instance is already gone.
  - `database.DeleteInstanceWithOwnedDefinition` does it: instance and overrides in one transaction, then definition cleanup in a second, best-effort transaction. A short read checks the definition first: the cleanup runs, and locks the definition row, only when `OwnerInstanceID` is set (#654).
- **Bulk operations:** bulk deploy, stop, clean and delete accept up to 50 instances for each request, and need `RequireDevOps`. They return a success or failure result for each instance.
  - Bulk delete uses the single-delete path (`InstanceHandler.deleteInstance`). It checks the status before `pre-instance-delete`.
  - It cleans first for instances that have cluster resources, through `Manager.CleanForDelete`. One conditional update sets `cleaning` and `stack_instances.delete_after_clean` (migration 57). The loser of two concurrent deletes gets 409.
  - `Update` never writes the flag. A failed clean and the interrupt recovery clear it.
  - The WebSocket message `instance.deleted` follows the row delete.

**Deploy and Helm**
- **Helm values merge order** (a later step overrides an earlier step):
  1. Shared values (cluster-scoped, by priority).
  2. Chart default values.
  3. Instance overrides.
  4. Template locked values (locked values always win).
  After the merge, the backend substitutes the template variables `{{.Branch}}`, `{{.ImageTag}}` (Docker-safe tag of the branch), `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}` and `{{.Owner}}`. `valuesBuilder` is the one pipeline for all values paths.
- **Deploy preview:** `GET /stack-instances/:id/deploy-preview` returns the merged values of each chart without a deploy.
- **Stop and clean:** a stop runs `helm uninstall --wait` for each chart. Its `--timeout` is the time left in the stop budget minus 10 s, at most 5 minutes. The stop ends only when Kubernetes has deleted the release objects, so a deploy right after the stop creates the PVCs again (issue #563). A clean and the orphaned-namespace cleanup do not wait: the namespace delete removes every object.
- **Namespace and Helm:** the deployer calls `k8s.Client.EnsureNamespace` before the first Helm install. If it succeeds, the install request has `NamespaceReady` and Helm gets no `--create-namespace`. With that flag, Helm always sends a namespace create. An admission policy that requires the `managed-by` label refuses that create. Without a ready namespace (no k8s client, or the check failed), Helm creates the namespace.
- **Namespace labels:** `k8s.Client.EnsureNamespace(ctx, namespace, instanceName)` sets these labels on create. It adds or corrects them on an existing namespace (deploy, rollback to a target). Other labels stay.
  - `managed-by=k8s-stack-manager`.
  - `k8s-stack-manager.io/instance-name=<instance name>` (the exact stack name for admission policies, because a namespace prefix is ambiguous). The backend sets it only when the name is a valid label value. Otherwise it logs a warning. A rename overwrites it.
  - The `NAMESPACE_LABELS` labels (`k=v,k=v`). `config.ParseNamespaceLabels` parses and validates them at startup. The keys `managed-by` and the instance-name key are reserved. `cluster.RegistryOptions.NamespaceLabels` passes them to each client.
- **Namespace access binding:** with `STACK_ACCESS_CLUSTER_ROLE` and `STACK_ACCESS_SERVICE_ACCOUNT` set (`<namespace>/<name>`), `EnsureNamespace` also applies the RoleBinding `k8s-stack-manager-access` (`k8s.AccessBinding`). This works for in-cluster clusters only. Both variables must be set, or neither. The leader worker `stack-access-reconcile` (`cluster.AccessReconciler`) applies it to existing stack namespaces once per term and every 10 minutes.
- **Chart sources:**
  - `ALLOWED_CHART_REPOS` is a comma list of `oci`, `https` or `http` URL prefixes (`models.NormalizeChartSourcePrefix`). Empty means all sources.
  - `models.CheckChartSource` checks the chart location: an OCI reference, a `--repo` URL plus the chart name, or a chart path. The location must equal a prefix or start with a prefix plus `/`. It must not contain `..`. A chart path with a scheme or a leading `/` next to a repository URL is rejected.
  - The check runs in `ChartConfig.Validate` and `TemplateChartConfig.Validate` (repository create and update).
  - It runs again before each Helm install: a deploy marks the chart failed, and a rollback to a target returns an error.
- **Rollback:** `POST /stack-instances/:id/rollback`
  - Without `target_log_id`, it rolls the Helm releases back one revision.
  - With `target_log_id` (a successful deploy of the instance), it upgrades each chart to the values snapshot and the recorded chart version of that deploy (`DeploymentLog.ChartVersions`). This restores values, not images.
  - `pre-rollback` fires in the rollback goroutine, with progress streaming like `pre-deploy`. It carries the pre-deploy chart list, so image gates subscribe to it. A rejection restores the previous instance status.
  - `rollback-completed` fires on either outcome. `post-rollback` fires only on success.
  - A successful rollback sets `LastDeployedValues` to the running values.
  - `values_drift` (rollback response, deploy preview, GET instance) shows that the stored overrides differ. The next deploy then undoes the rollback.
- **Deployment history (#626):** `GET /stack-instances/:id/deploy-log` takes `limit`, `offset`, `cursor`, `view`, `action` and `status`.
  - Without `view` (or `view=full`), each row has all fields, also `output` (`stackctl stack logs` reads it). An empty `output` has no key (`omitempty`). The response has `next_cursor` when more rows exist, also on the offset path. `limit` is at most 100 (a larger value counts as 100).
  - `view=summary` returns the row columns only (no `output`, `values_snapshot` or `chart_versions`). Without a cursor, it adds `highlights`: `running` (the newest running log), `current` (the deploy whose values run now: the newest successful or partial deploy; for a newer successful rollback, its target) and `latest_failure` (the newest `error` log, only when it is newer than that deploy or rollback). The highlights ignore the filters. A highlights error omits `highlights` (the UI then computes them from the loaded rows).
  - `action` and `status` are comma lists. An unknown value gives 400. `total` counts the filtered rows; a cursor page has `total: -1`.
  - Each row has `started_by` (the username of `user_id`, one batch lookup).
  - `GET /stack-instances/:id/deploy-log/:logId` returns one log with `output` (no values snapshot). A log of another instance gives 404.
  - The web UI (`DeploymentHistory`, `useDeployHistory`) shows the highlights pinned on top, then 10 rows with "Show more", action and status filters, and folds 3 or more adjacent successful deploys of one branch into one row. A row loads its output when it opens. The rollback targets come from a separate request (`action=deploy&status=success&limit=50`).
- **Pod problems:**
  - During the Helm and stabilize phases of a deploy or rollback, `deployer.podEventWatch` polls namespace Warning events and container waiting reasons every 10s (`k8s.Client.ListPodProblems`).
  - It writes new ones as `Pod event:` lines to the deploy log, grouped by reason and object with a count (`(x18)`). The most relevant one (quota first) goes into the error message.
  - The namespace ResourceQuota and LimitRange are applied after the namespace is ensured and before the first Helm install (deploy, redeploy, rollback to a target).
  - A partial deploy gives the deploy log status `partial` (`models.DeployLogPartial`).
- **Resource quotas:** per-cluster quotas (CPU, memory, storage, ephemeral storage, pods) use Kubernetes ResourceQuota and LimitRange objects. An admin configures them through the API.
  - `k8s.Client.EnsureLimitRange` takes the per-container fields: `container_{cpu,memory,ephemeral_storage}_{request,limit}` and `container_{cpu,memory}_max` (migration 58 adds the columns).
  - It never copies a namespace total into a container default. Without a container default it uses a fallback (CPU 100m/500m, memory 128Mi/512Mi, ephemeral storage 128Mi/1Gi), capped at the namespace total and the container max.
  - Validation (`quotaOrderRules` in `models/quota_merge.go`): request <= limit <= max for each container, and a container default <= the namespace total.
  - `ephemeral_storage_limit` sets `requests.ephemeral-storage` and `limits.ephemeral-storage`.
  - A user without the admin or devops role can set an instance quota override only at or below each cluster quota value, also for the per-container fields (`models.CheckOverrideWithinClusterQuota`, 403). The check runs on write and on clone. A value equal to the stored override passes. `pod_limit: 0` counts as no limit.
  - The quota monitor raises quota warnings through the notifier. The `quota-warning` hook constant is defined but not dispatched.
  - `GET /stack-instances/:id/quota` (any user who can view the instance, #629) returns `effective` (the cluster quota merged with the override, `MergeQuotaOverride`), `cluster`, `override`, `sources` (each field: `override`, `cluster` or `none`, `models.QuotaSources`), `cluster_id`, `can_modify`, and `valid` and `invalid_reason` (`models.ValidateEffectiveQuota`, the check of the deployer: a deploy fails while it is false, and the section shows a warning). It shows the cluster quota to every viewer of the instance. The instance page shows it in the "Resource quota" section (`components/InstanceQuota`). Owner, devops and admin edit or remove the override there. The role `user` sees the cluster value as the upper limit of each field.
  - A quota change applies at once, in the background (`handlers/quota_apply.go`).
    - A cluster quota save writes the effective quota (cluster quota merged with the instance override) to the namespace of each instance, and the cluster quota to the other stack namespaces. It skips Terminating namespaces.
    - An override set or delete writes the effective quota to the namespace of a deployed instance (not draft or cleaning).
    - Each write reads the stored cluster quota and override again while it holds a lock for the namespace (`writeNamespaceQuota`), so the last write of a replica has the latest state. Across replicas, and against a concurrent deploy, the last writer wins.
    - The backend skips a namespace when a read fails or the merged quota is not valid, and logs the reason. The next deploy applies the quota. An empty effective quota writes nothing, so the old objects stay.

**Access control**
- **Auth:** JWT with the header `Authorization: Bearer <token>`. The middleware puts `userID`, `username` and `role` into the Gin context. Refresh tokens rotate through `/auth/refresh`. Logout blocks tokens in `sessionstore`. See `docs/claude/auth-sessions.md`.
- **Roles:** `user`, `devops`, `admin`.
- **User roles API:** `PUT /users/:id/role` (admin) changes the role of a local user.
  - An SSO user gives 409 (the identity provider sets the role at each login). The own role gives 403. An unchanged role is a no-op.
  - Delete, disable, enable and role change are guarded (`UserRepository.UpdateRole`, `SetDisabled`, `DeleteGuarded`). One transaction locks the admin rows (by ID), then the target.
  - The caller must still be an enabled admin (403). The last enabled admin cannot be demoted, disabled or deleted. Over HTTP the own-account checks (403, 400) and the enabled-admin check (403) come first, so the repository guard (`ErrLastAdmin`, 409) is defense in depth.
  - An unchanged role answers 200 with `changed: false` and writes no audit entry (`middleware.SkipAudit`).
  - Handlers use the stored ID after `FindByID` (MySQL compares IDs without case).
  - `UserRepository` has no full-row update. Use the targeted writes (`UpdatePassword`, `UpdateProfile` for the SSO sync and link, `UpdateRole`, `SetDisabled`), so concurrent changes are not lost.
  - Migration 50 indexes `users.role`.
  - `guardedAdminChange` runs its transaction again on MySQL error 1213 (deadlock), at most 3 attempts, with a random 10 to 50 ms wait (`database/lock_retry.go`, #639). A lock wait timeout (1205) is not retried. `UpdateProfile` (the SSO role sync) with a role change locks the admin rows first, in the same order (`lockAdminRows`), and also retries on 1213. The side effects (token block, refresh-token revoke, WebSocket disconnect, audit) run once, in the handler, after the commit.
  - A change runs `revokeUserAccess` (user block, refresh tokens, WebSocket disconnect). The audit action is `change_role` with `old_role` and `new_role`.
- **Definition access:**
  - Create definition, import, export and add or remove chart need `RequireDevOps`.
  - Update, delete, upgrade and chart update need the owner, devops or admin (`requireDefinitionModify`, 403).
  - A user without devops or admin can change chart default values and deploy order. That user cannot change the chart name, repository, path or version (`chartSourceChanged`, 403).
  - The UI hides Create Definition, Import and Add/Remove Chart for the role `user`. It shows the chart source read-only.
- **Template access:** edit, delete, publish, unpublish and chart changes need `RequireDevOps` and the template owner or an admin. Bulk template operations need `RequireDevOps`. Instantiate and quick deploy are open to every authenticated user.
- **Instance access:** deploy, stop, clean, delete, rollback, overrides, TTL and custom actions need the instance owner, a devops user or an admin.

**Templates and definitions**
- **Template versioning (draft and release):**
  - The template row and its template charts are the working copy (draft). You can edit them while the template is published. Edits never change what users get.
  - `POST /templates/:id/publish` (optional `{"version","change_summary"}`, default: the version of the working copy) stores a snapshot. An existing version gives 409. An unchanged working copy creates no snapshot (idempotent). Publish runs in one transaction that locks the template row.
  - Use Template, Quick Deploy, definition upgrades and deploy-time locked values read the latest snapshot (`template_releases.go`). An unpublished template, or one that was never published, gives 409 "Template has no published version".
  - `GET /templates/:id` adds `published_version`, `published_version_id`, `published_charts` and `has_unpublished_changes`.
  - The version diff accepts `working` on either side.
  - `PUT /templates/:id` is a partial update.
- **Definition names:** unique for each owner (application check, no unique index). Create, rename and template instantiate return 409 for a duplicate. Import renames to `<name> (imported)`, `<name> (imported 2)`, and so on. Quick deploy uses `<name> (2)`, and so on.
- **Import and export:** you can export a definition as a JSON bundle and import it to create a new definition with charts.
- **Upgrade check:** `GET /stack-definitions/:id/check-upgrade` and `POST /:id/upgrade` share `upgradeTargetChart` (`handlers/stack_definitions.go`): the chart as the upgrade writes it. `chart_diffs` compares the definition charts with it (also source repository and build pipeline), and `changes` is grouped from `chart_diffs`, so both agree. Without an upgrade, `reason` is `no_source_template`, `no_published_version` or `up_to_date`.
- **Template link:** `PUT /stack-definitions/:id/template-link` with `{"template_id","version"}` (devops or admin, `handlers/definition_template_link.go`) writes only the link columns (`SetTemplateLink`), and sets or removes `source_template_id` and `source_template_version` of an existing definition. The template must be published (409); the version must be a published version (400, default: the latest). An empty `template_id` removes the link. Charts and values do not change. A linked definition gets the template rules (required charts, locked values at deploy, both from the latest published version; the linked version is the base of the upgrade check) and counts in the analytics template usage. Audit action `link_template`. Export writes `definition.source_template` (`id`, `name`, `version`); import looks it up by ID, else by a unique name among the published templates, and returns `warnings` instead of a failure when the link cannot be set. The definition page has a "Template link" dialog (`TemplateLinkDialog`, devops and admin).
- **Shared values:** per-cluster Helm values for all instances in that cluster. They merge by priority (lowest first) before the chart defaults and the instance overrides.
- **Bulk template operations:** bulk delete, publish and unpublish accept up to 50 templates for each request. They return a result for each template.

**Clusters and operations**
- **Multi-cluster:** register clusters through `/api/v1/clusters` with kubeconfig data (encrypted at rest through `pkg/crypto`) or a kubeconfig path.
  - `ClusterRegistry` manages the K8s and Helm clients of each cluster. The health poller monitors the status. The registry reads the cluster row on its own context with a 10s timeout, never the request context.
  - A cluster with `use_in_cluster` uses the backend service account and has no API server URL.
    - The Kubernetes client uses only `rest.InClusterConfig()`, and Helm gets `KUBECONFIG=/dev/null` (`HelmClient.command`), so neither reads `$KUBECONFIG` or `~/.kube/config`.
    - Outside a pod (`make dev-local`, a binary on a laptop), such a cluster is unreachable (`k8s.ErrInClusterUnavailable`). For local development, `IN_CLUSTER_FALLBACK_KUBECONFIG=<path>` gives both clients that kubeconfig, with a warning in the log (#582). In Docker Compose the path is in the container (`make dev-k8s`: `/root/.kube/config`).
  - An instance targets one cluster (or the default).
  - Per-cluster registry credentials (`registry_url`, `registry_username`, `registry_password`) give automatic image pull secret provisioning at deploy time and a refresh every 4 hours (`SecretRefresher`).
- **Git provider detection:** by the exact host of the URL. `dev.azure.com` and `*.visualstudio.com` mean Azure DevOps. `github.com` means GitHub. `gitlab.com`, or the custom `GITLAB_BASE_URL` origin, means GitLab. With a custom origin, `gitlab.com` URLs are not supported. A known provider without credentials gives HTTP 503.
- **Cleanup policies:**
  - `PUT /admin/cleanup-policies/:id` is a partial update. Only the fields in the body change. The backend validates the merged policy.
  - A policy runs a cron-scheduled action (stop, clean, delete) on instances that match a condition: `status:`, `idle_days:` (days without a deploy; a running or partial stack whose expiry is in the future never matches), `age_days:` (creation age), `stopped_days:` (time since `StoppedAt`), `cleaned_days:` (time since `CleanedAt`, drafts only), `ttl_expired`, `expires_within_hours:` (no expiry, or an expiry at most N hours away, N 0 to 8760; an extended TTL keeps a stack out of a scheduled stop, for example `status:running,expires_within_hours:12` on Fridays; "No expiry" still matches).
  - A policy targets one cluster or "all".
  - Only the leader replica runs the cron jobs. It reloads on the policy changes it handles, when it becomes leader and every minute. A scheduled run reads the policy again.
  - Any replica can run a manual run. A manual run supports dry-run mode.
  - `StoppedAt` is set when a stop completes (API, TTL reaper, policy). A deploy or a clean clears it.
  - `CleanedAt` (migration 59) is set when a clean completes. A deploy clears it. A clean also clears `LastDeployedAt`, so `idle_days` counts from the creation after a clean. Use `cleaned_days` for "cleaned N days ago".
- **Orphaned namespaces:** `/admin/orphaned-namespaces` lists `stack-*` namespaces without an instance. Each entry has a `managed` flag (label `managed-by=k8s-stack-manager`). Delete of an unmanaged namespace needs `?confirm=<full name>`, else 409.
- **Cluster health units:** `GET /clusters/:id/health/summary` adds `requested_cpu` and `requested_memory`. `GetClusterRequests` sums the pod requests with the scheduler rule. It reads pods from the API server cache. This is not the real use, and the dashboard does not list pods. The frontend parses quantities with `utils/quantity.ts` and shows cores and binary units.

**Notifications and UI**
- **Notification channels:** DevOps and admin users register outbound webhook channels (`/admin/notification-channels`, `RequireDevOps`). Each channel has event subscriptions, a test send and delivery logs.
  - Optional `filters`: `instance_name_patterns` (globs through `path.Match`), `owner_ids`, `definition_ids`, `cluster_ids`. Filters combine with AND, values inside a filter with OR. An empty filter means all.
  - `channel.Dispatcher` matches the filters against `EventPayload.Instances`. An event without an instance goes only to channels without filters. `cleanup.policy.executed` goes to a channel when one affected instance matches.
  - A bad glob gives 400. An unknown ID adds a `warnings` entry.
  - Delivery (#649): each channel has its own worker and queue (100 events). A slow or down channel does not delay the other channels. Events to one channel stay in order. The notifier intake queue holds 1000 events. Its worker finds and filters the channels, encodes the body once per format and queues one job per channel.
  - The channel worker reads the channel again before each send, so a new URL, secret or format applies to the queued events. A deleted or disabled channel gets nothing and writes no delivery log. When the read fails, the worker sends with the queued copy and warns once per channel per minute.
  - The counter `notification.channel.dropped` counts the events that the backend does not send (`event_type`, `channel_id` (empty for the intake queue), `reason`: `queue_full`, `shutdown` or `channel_removed`). The backend logs at most one drop warning per channel and reason per minute, with the channel name and the drop count. "delivery failed" also gives at most one warning per channel per minute, with the count. Each delivery still writes its delivery log.
  - `webhook_url` must be a valid https URL with a host (400 "Webhook URL must start with https://" or "Webhook URL is not a valid URL"). Error texts, delivery logs and log lines never contain the URL, because it can hold a token.
  - `format`: `generic` (default, the event JSON) or `teams` (a Microsoft Teams Adaptive Card envelope for a Teams workflow made from the template "Post to a channel when a webhook request is received"). An unknown format gives 400. The signature covers the sent body. The test send uses the format. Migration 60 adds the column.
  - Payload (`channel.EventPayload`): instance events also carry `trigger` (`type` `user`, `cleanup-policy` or `ttl`, `id`, `name`; the struct of the hook envelope), `owner_display_name`, `namespace`, `branch`, `cluster_id`, `cluster_name`, `site_url` and `instance_url`, each omitted when empty. `user_display_name` is the owner (`System` for system events) and is deprecated.
    - The callers pass the trigger with `NotifyInstance` options (deployer `m.triggerFor`, TTL reaper `ttl`, cleanup policy, handler). An operation that a restart interrupted and the expiry warning have no trigger.
    - `site_url` is the primary Ingress URL (the rule of `GET /stack-instances/:id/status`), only for `deployment.success`, `deployment.partial` and `rollback.completed`. A failed status read gives no URL.
    - `instance_url` is `<APP_PUBLIC_URL>/stack-instances/<id>`, not for `instance.deleted`.
  - `APP_PUBLIC_URL`: public https base URL of the web UI (a host, optional path prefix). http, a same-origin path, a query or a fragment stop the start. Empty: no `instance_url` and no "Stack Manager" button. The Helm chart sets `https://<ingress.host>` when the ingress has TLS and `backend.env.APP_PUBLIC_URL` is not set; a set value wins.
  - `teams` card (Adaptive Card version 1.5, for the TextBlock style `heading`) of an instance event: the title `<mark> <title> — <stack name>` (heading, bolder, medium, event colour), the message, the facts Namespace, Branch, Cluster, Triggered by and Owner (each only when set), and the buttons "Open site" (`site_url`) and "Stack Manager" (`instance_url`). "Triggered by" is the username, "TTL expiry", "Cleanup policy <name>" or "a user". System events (`cleanup.policy.executed`, `quota.warning`, `secret.expiring`, the test send) keep the facts Event, By and Time.
- **Notifications:**
  - In-app notifications for deploy, stop and clean events. Each user has notification preferences and an unread count for the badge.
  - Dispatch uses the `notifier` package. The WebSocket message `notification.new` goes only to the sockets of the notified user (`Hub.BroadcastToUser`).
  - Instance events use `Notifier.NotifyInstance(models.NotificationTarget, ...)`. The receivers are the owner and the followers (no duplicates). Each receiver is filtered by the own event-type preferences (`DisabledUserIDs`, one query, fail open). The actor is not excluded. One channel dispatch for each event.
  - `NotifySystem` and `NotifySystemForInstances` (admin and devops) apply the preferences too.
  - Read the followers with `FollowerIDs` before an instance delete, and put them in `NotificationTarget.FollowerIDs`.
  - Each in-app notification of an instance event stores its trigger (#652, migration 64: `trigger_type`, `trigger_id`, `trigger_name`, nullable). `NotifyInstance` takes it from the `WithTrigger` option, else from the context (`hooks.WithTrigger`). `Notify` and `NotifySystem` store no trigger.
  - The API and `notification.new` return `trigger` (`type` `user`, `cleanup-policy` or `ttl`; `id`; `name`). A notification without a trigger has no `trigger` field. The bell menu and the Notifications page show "by alice", "by you" (the receiver did the action), "by expiry" (TTL or stop time) or "by Cleanup policy <name>" after the time (`formatTrigger` in `utils/notificationHelpers.tsx`).
  - Create, clone and quick deploy send `instance.created` through `notifyInstanceCreated` (`handlers/stack_instances.go`), with the caller as the trigger (#650).
  - A cleanup-policy stop or clean gives one notification (#651). The deploy manager sends it when the operation ends: `cleanup.policy.stop` or `cleanup.policy.clean` with a `cleanup-policy` trigger, else `deployment.stopped` or `clean.completed`. The scheduler sends only the summary `cleanup.policy.executed`. The TTL reaper sends `stack.expired`, then `deployment.stopped`.
  - `NotificationRepository.Create`, `MarkAllAsRead` and `MarkAsRead` run again after a MySQL deadlock (1213, `retryOnDeadlock`, 3 attempts): a new notification and "Mark all read" of the same user lock the same index ranges (#648). Each is one idempotent statement, and the ID is set before the first attempt, so a retry never stores a second row. Migration 62 drops the redundant `is_read` index; `idx_notifications_user_read_created` serves the queries.
- **Favorites:** users can bookmark templates and instances (`UserFavorite` entities). A favorite does not follow the instance.
- **Analytics and dashboard:**
  - `/analytics/*` (devops) gives read-only aggregation of instance counts, deploy stats, template usage and user activity. `/analytics/users` needs admin.
  - `/dashboard` serves the landing page overview. Cluster health is for admin and devops only. The widget is hidden for the role `user`.
  - Per-user deploy counts use `DeploymentLog.UserID` (who started the deploy). Migration 49 backfills the instance owner. The counts survive instance deletes.
  - The stack list (Dashboard) updates live. `deployment.status` changes a row.
  - An event for an instance that is not in the list reloads the list once for each instance ID, 500 ms after the first event. The events are `deployment.status` and `instance.created` (`Manager.BroadcastInstanceCreated` sends it to all clients after create, clone and quick deploy).
  - A `deployment.status` with status `running` fetches the card URL again and reloads the list once (new deploy time).
  - `instance.deleted` removes the row. The backend sends it after every delete: after a clean by the deploy manager, and after a direct delete (API, bulk delete or cleanup policy) by `Manager.BroadcastInstanceDeleted`.
- **Branding:** `APP_TITLE` (default `K8s Stack Manager`), `APP_LOGO_URL` (empty means the built-in `/logo.svg`), `APP_FAVICON_URL` (default `/favicon.svg`; both files are in `frontend/public/`). `config.BrandingConfig` validates them at startup: a same-origin path or an https URL. It refuses `http:`, `javascript:`, `data:`, `//host` and whitespace. The public `GET /api/v1/ui-config` is rate-limited and has `Cache-Control: public, max-age=300`.
- **Setup wizard:** the frontend shows a first-run checklist until a cluster, a template and an instance exist, or until the user dismisses it (localStorage).
- **Generic design:** no company-specific hard-coding. Branding and all configurable values use environment variables.
- **Hooks:** put organization-specific behavior (Slack, database refresh, approval gates) in hook subscribers, not in the core. See `docs/claude/hooks-actions.md` and `EXTENDING.md`.

### API Route Groups

| Group | Prefix | Description |
|-------|--------|-------------|
| Auth | `/api/v1/auth` | Login, register, current user, refresh, logout, logout-all |
| OIDC Auth | `/api/v1/auth/oidc` | OpenID Connect config, authorize, callback, CLI auth and token |
| Dashboard | `/api/v1/dashboard` | Landing page overview aggregation |
| Templates | `/api/v1/templates` | CRUD, publish, unpublish, instantiate, clone (optional `name`) |
| Template Versions | `/api/v1/templates/:id/versions` | Version history listing, detail, diff |
| Quick Deploy | `/api/v1/templates/:id/quick-deploy` | One-click template deployment |
| Bulk Template Ops | `/api/v1/templates/bulk` | Bulk delete, publish, unpublish templates (up to 50, devops) |
| Stack Definitions | `/api/v1/stack-definitions` | CRUD, nested chart management, import and export, template link, upgrade check and upgrade |
| Stack Instances | `/api/v1/stack-instances` | CRUD, clone, values export, deploy, deploy-preview, stop, clean, rollback, status, pods, deploy-log (list, one entry, values), recent, compare, extend TTL, follow and unfollow, custom actions (list, invoke, job log, action runs) |
| Bulk Operations | `/api/v1/stack-instances/bulk` | Bulk deploy, stop, clean, delete (up to 50 instances, devops) |
| Value Overrides | `/api/v1/stack-instances/:id/overrides` | Per-chart value overrides |
| Branch Overrides | `/api/v1/stack-instances/:id/branches` | Per-chart branch overrides (list, get, set, delete for each chart) |
| Quota Overrides | `/api/v1/stack-instances/:id/quota-overrides` | Per-instance resource quota overrides (`PUT` replaces all fields); the effective quota is `GET /api/v1/stack-instances/:id/quota` |
| Git | `/api/v1/git` | Branch listing, validation, provider status |
| Audit Logs | `/api/v1/audit-logs` | Filterable audit log viewer (devops or admin) and export (admin) |
| Users | `/api/v1/users` | User management (admin): list, delete, disable, enable, password reset, role change |
| API Keys | `/api/v1/users/:id/api-keys` | API key management |
| Admin | `/api/v1/admin` | Orphaned namespace detection and cleanup (admin) |
| Cleanup Policies | `/api/v1/admin/cleanup-policies` | Cron-based cleanup policy management (admin) |
| Notification Channels | `/api/v1/admin/notification-channels` | Outbound webhook channels, event types, subscriptions, test, delivery logs (devops) |
| Clusters | `/api/v1/clusters` | Multi-cluster registration (admin), health, test-connection, quotas, utilization |
| Shared Values | `/api/v1/clusters/:id/shared-values` | Per-cluster shared Helm values (admin) |
| Favorites | `/api/v1/favorites` | User bookmarks |
| Notifications | `/api/v1/notifications` | List, read and unread, count, preferences |
| Analytics | `/api/v1/analytics` | Usage overview, template stats, user stats |
| UI Config | `/api/v1/ui-config` | Public branding values (title, logo, favicon), the TTL default of the create forms and the TTL limit (`max_instance_ttl_days`) |
| Health | `/health/*` | Liveness and readiness |
| WebSocket | `/ws` | Real-time status and log events. Replicas share them through `ws_events` when `WS_FANOUT_ENABLED=true`. `RedactWSToken` removes the token from logs. |
