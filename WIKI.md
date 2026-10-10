# K8s Stack Manager — Wiki

## Concepts

### Stack Template
A reusable blueprint created by DevOps engineers. Contains a set of Helm chart configurations with default values. Templates can be **published** for developers to use, and individual chart values can be **locked** to prevent modification.

**Clone.** `POST /templates/:id/clone` (devops and admin) copies the template and its charts into a new draft that belongs to the caller. The optional body `{"name": "..."}` sets the name (trimmed; an empty name gives 400). Without a name the clone is called `<source name> (Copy)`. Template names do not have to be unique.

### Template Life Cycle (Draft and Release)

A template has a **working copy** and a **version history**.

- **Working copy (draft).** The template fields and its charts. DevOps users edit the working copy, also while the template is published. An edit never changes what users get. `PUT /templates/:id` is a partial update: only the fields in the body change.
- **Who can change a template.** Edit, delete, publish, unpublish and chart changes need the DevOps role and the template owner or an admin (same rule as the bulk operations). Other users get 403.
- **Scripts.** A deployment script that changes a template (for example `stackctl template update-chart`) changes only the working copy. Publish a new version afterwards (`POST /templates/:id/publish` with a new `version`), else users keep getting the old content.
- **Publish.** `POST /templates/:id/publish` with the optional body `{"version": "x.y.z", "change_summary": "..."}` stores the working copy as a **snapshot** (a version). Without `version`, the API uses the version of the working copy. The working copy then takes the published version.
  - A version string is unique per template. A version that already exists gives 409 `Version x already exists`. Set a new version to publish changes.
  - When the working copy equals the latest snapshot (same content and version), publish creates no snapshot and returns 200 (`snapshot_created: false`). Publish is idempotent.
  - Bulk publish and create with `is_published: true` follow the same rules.
- **What users get.** Use Template, Quick Deploy, the definition upgrade (check-upgrade and upgrade) and the template locked values at deploy time read the **latest snapshot**, never the working copy.
- **Unpublish.** `POST /templates/:id/unpublish` hides the template. Use Template and Quick Deploy return 409 `Template has no published version`, and upgrades are not offered. The history stays. A template that was never published also gives 409, for the owner too.
- **Template detail.** `GET /templates/:id` returns the template fields plus `published_version`, `published_version_id`, `published_charts` (the snapshot charts; null without a snapshot) and `has_unpublished_changes`. Only the owner and admins get the working copy in `charts`. For other users `charts` is the same as `published_charts` (an empty list without a snapshot), `version` is the published version (empty without a snapshot) and `has_unpublished_changes` is false, so draft information stays hidden.
- **Find by name.** `GET /templates?name=<name>` filters by exact name (same as the stack-definitions filter). The response keeps the paged envelope. stackctl uses it to resolve template names.
- **Unpublished changes.** `GET /templates/:id/versions/diff?left=<version id>&right=working` compares a snapshot with the working copy. Either side can be `working`. Only the owner and admins can compare the working copy (others get 403).
- **Old-format snapshots.** A latest snapshot in the old format (no chart version or chart path) always counts as changed. The next publish stores the full format and can reuse the version string of that snapshot once.
- **Use Template overrides.** `chart_overrides` replace chart default values in the new definition. Keys are `published_charts[].id`, working copy `charts[].id` (mapped by chart name) or chart names. Keys for charts that are not in the snapshot are ignored.
- **Upgrade.** Check-upgrade returns `chart_diffs` (same shape as the version diff): left is the definition now, right is the latest snapshot.
- **Existing data.** The upgrade migration stores the working copy as a new snapshot for each template that is published, or used by a stack definition, when its latest snapshot is missing (published templates only), in the old format, or different from the working copy. So users keep getting the content they got before the upgrade. These snapshots have the summary `Backfill: release of the working copy at upgrade` and can repeat an existing version string. A template without a version gets `1.0.0` (snapshot and working copy). Duplicate versions from older releases stay in the history.
- **Value comparison.** Publish, `has_unpublished_changes` and the diffs ignore trailing spaces and line breaks in values.

### Stack Definition
A concrete collection of Helm chart configurations. Created by instantiating a template or from scratch. Owns the chart configs (chart name, repository, version, default values).

- Definition names are unique per owner. Create, rename and template instantiate return 409 for a name that the owner already uses.
- Import does not fail on a used name. It uses `<name> (imported)`, then `<name> (imported 2)`, and so on. The response shows the final name.
- Quick deploy creates one definition for the new instance and sets `owner_instance_id`. When you delete that instance, the definition and its charts are deleted too, if no other instance uses the definition. If the name is used, quick deploy names the definition `<name> (2)`, `<name> (3)`, and so on.
- Editing a quick deploy definition (for example adding a chart) does not keep it. It is still deleted with its owner instance when no instance uses it. To keep a setup, create your own definition (for example export and import it).
- Older databases can contain duplicate names. They stay until you rename or delete them.
- On MySQL the check is not case-sensitive (the default collation): `My-Def` and `my-def` are the same name. The in-memory test repositories compare exactly.
- The paged list (`GET /stack-definitions`) does not load the charts. It returns `chart_count` (one grouped count query for the page); the definitions page shows it in the Charts column.

### Stack Instance
A developer's working copy of a stack definition. Each instance has:
- An **owner** (the developer)
- A **branch** (Git branch for the deployment)
- **Value overrides** per chart (merged on top of chart defaults)
- An auto-generated **namespace** (`stack-{instance-name}-{owner}`)

**List filters.** `GET /stack-instances` accepts these filters. They combine (AND). `total` is the number of instances that match.
- `status`: one of `draft`, `queued`, `deploying`, `stabilizing`, `running`, `stopping`, `stopped`, `cleaning`, `partial`, `error`. Another value gives 400.
- `cluster_id`, `definition_id`: an ID.
- `owner`: `me` (you), a username, or a user ID. A value in UUID form is matched as a user ID first, then as a username. An unknown owner gives an empty list.
- `name`: the exact instance name.

`GET /stack-definitions` accepts `owner` and `name` in the same way.

Both list endpoints are paged (`page`, `pageSize`; default 25, maximum 100). With `owner=me` or `name`, `total` is the real number of matches, not the page size.

**Names in responses.** Instance responses include `owner_username`, `definition_name` and `cluster_name`. Definition and template responses include `owner_username`. A field is omitted when the owner, definition or cluster no longer exists (for example, after a delete). `cluster_name` is also omitted when `cluster_id` is empty (older instances).

### Stack Instance Lifecycle

**Names.** An instance name must be a DNS label (RFC 1123): lowercase letters `a-z`, digits `0-9` and `-`. It must start and end with a letter or a digit. The maximum length is 50 characters. Charts build host names from `{{.InstanceName}}`, so other characters break the Ingress. The API checks the rule on create, clone, quick deploy and rename (400). Older instances with other names keep working; an update that keeps the name does not fail.

**Clone.** `POST /stack-instances/:id/clone` accepts an optional body `{"name": "...", "branch": "...", "ttl_minutes": N}`.
- Without a name, the API uses the first free name of `<name>-copy`, `<name>-copy-2`, and so on.
- The clone gets the cluster, the TTL, the value overrides and the branch overrides of the source. It gets the quota override only when the caller owns the source or is admin or devops; any other user gets the cluster quota (a quota override is a resource grant, not configuration). An owner without the admin or devops role gets the quota override only when it stays within the cluster quota (the same rule as setting a quota override, see below); otherwise the clone uses the cluster quota, the response has a `warning` field that says so, and the backend logs the skip. The clone still succeeds.
- `branch` and `ttl_minutes` replace the values of the source. The clone is a draft and belongs to the caller.

**Extend.** `POST /stack-instances/:id/extend` never makes the expiry earlier and never changes `ttl_minutes`.
- `{"minutes": N}` adds N minutes to the current expiry. When the instance already expired or has no expiry time, it adds N minutes to now. N must be greater than 0.
- An empty body adds the instance TTL (`ttl_minutes`) in the same way.
- The new expiry is at most now + 30 days.
- Deprecated: a body with only `ttl_minutes` keeps the old behaviour (expiry = now + `ttl_minutes`, and `ttl_minutes` changes). stackctl 0.4.0 and earlier send this body. The response has a `Warning` header.
- The response is the instance with the new `expires_at`.

**Denied deploy.** When a `pre-deploy` hook with `failure_policy: fail` denies a deploy, the instance gets the status `error`. The reason, `pre-deploy hook "<name>" denied the deployment: <subscriber message>`, is in the `error_message` of the instance (shown on the detail page), in the `error_message` of the deploy log, and as an `ERROR:` line in the deploy log output (`stackctl stack logs`). Hook progress lines (`LOG:`) are also kept in the output. A hook that cannot be reached gives `pre-deploy hook "<name>" failed (unreachable or timed out)`. The subscriber URL is never shown.

**Pod problems in the deploy log.** While Helm installs the charts and while the pods stabilize (deploy and rollback), the backend reads the Warning events and the container states of the namespace every 10 seconds. Each new problem gives one `Pod event: <reason> <object>: <message>` line in the deploy log (live and stored): pods that cannot be created (`FailedCreate`, for example `exceeded quota` of the ResourceQuota or a LimitRange `forbidden`), `FailedScheduling`, `FailedMount`, image pull errors (`ErrImagePull`, `ImagePullBackOff`), `CrashLoopBackOff` and similar. A repeated event is shown once; at most 5 new lines per poll and 30 per operation. When the operation fails, the most relevant problem (a quota or LimitRange rejection first) is added to the error message: `... (pod event: FailedCreate job/<name>: ... exceeded quota: ...)`. A readiness timeout warning gets it too. The events count from the start of the operation; the container states are the current state of the pods, so a container that was in `CrashLoopBackOff` before the operation also shows. Stop and clean do not wait for pods and do not read the events. The backend needs `list` on `events` and `get`/`list` on `pods` in the stack namespace; without it the log has no pod event lines and the backend logs one warning per operation.

**Rollback.** `POST /stack-instances/:id/rollback`:
- Without a body, each Helm release goes back one revision.
- With `{"target_log_id": "<log id>"}`, each chart gets the values of that deploy again (`helm upgrade --install` with the stored values snapshot). The chart version is the version that the deploy recorded. Older deploy logs have no recorded versions; then the current chart version is used. A deploy with an empty chart version records no version, so the rollback installs the newest chart. Charts that the target deploy did not include do not change.
- A rollback to a target restores the stored **values** (including the shared and locked values of that time), not the images. Image tags that are branch names can point to newer images now.
- Pre-deploy hooks do not run for a rollback. Subscribe image gates to `pre-rollback`. It gets the same chart list as `pre-deploy` (name, version, branch, `image_tag`) and the metadata `rollback_mode`, `target_log_id` and `target_branch` (see `backend/docs/hooks.md`).
- In both modes a release stuck in `pending-*` is cleared before its upgrade or rollback (as for a deploy), and the instance waits for pod readiness (`stabilizing`) when readiness gating is on.
- The `pre-rollback` hook runs in the background after the API answered 202. If it rejects the rollback, the rollback log ends with status `error` and the reason, and the instance gets its previous status back.
- A one-revision rollback that fails on a chart records the values of the charts that it already rolled back, so the deploy preview stays correct.
- The target must be a successful deploy of the same instance. Another log returns 400; an unknown log or a log of another instance returns 404.
- The rollback does not change the stored value or branch overrides. After a rollback, the deploy preview compares against the values that now run (a failed rollback records the charts that it already upgraded). `values_drift: true` (in the rollback response for a target, in the deploy preview and in `GET /stack-instances/:id`) means that the next deploy applies the stored overrides again and undoes the rollback.
- Each deploy and rollback log records the branch (`branch`). A rollback to a target records the branch of the target deploy.

**Interrupted operation.** When the backend replica that runs a deploy, rollback, stop or clean stops suddenly (for example an out-of-memory kill or a node failure), the instance stays `deploying`, `stabilizing`, `stopping` or `cleaning` for some time. The backend then sets the instance to `error` with the message `Interrupted: the server that ran this operation stopped. Deploy again.` (for a stop: `Stop again.`, for a clean: `Clean again.`). The deploy log also ends with this error, and the owner and the followers get the failure notification. This happens when the operation passed its deadline (the longest time the operation can take: hook timeouts, one deploy timeout per chart, the readiness wait and the blocking `post-deploy` hooks, plus 5 minutes), and the replica sent no heartbeat for 2 minutes. When the slow replica finishes later, it does not change the result. An operation on a running replica is never changed. To end the state earlier, use Stop. A normal restart (rolling update) ends the running operations at once with an error.

**Expiry warning.** About 30 minutes before the expiry, the owner and the followers get one "Stack expiring soon" notification. The backend records the warning in the database (`expiry_warned_at`), so a restart or a second backend replica does not send it again. A new expiry time (deploy, extend, TTL change) allows a new warning.

**Expiry.** When the TTL reaper stops an expired instance, the owner and the followers get one "Stack expired" notification (`stack.expired`; notification channels can subscribe to it). The stop then gives the usual "Stack stopped" (or "Stop failed") notification.

**Redeploy.** `POST /stack-instances/:id/deploy` also works for a `running` instance. It upgrades the releases with the current values. With a TTL, the expiry becomes now + `ttl_minutes`, unless the current expiry is later: a redeploy never makes the expiry earlier.

**Delete.** `DELETE /stack-instances/:id` and bulk delete (`POST /stack-instances/bulk/delete`) apply the same rules to each instance. An instance with cluster resources (`running`, `partial`, `stopped`, `error`) is cleaned first (Helm uninstall and namespace delete), and the row is deleted when the clean completes: the single delete gives 202 with the `log_id` of the clean, bulk delete gives a `success` result with the `log_id`. Note: this `success` means that the clean started. The row is removed when the clean finishes; then the WebSocket message `instance.deleted` (`instance_id`, `log_id`) goes to all clients. A `draft` instance is deleted at once (204). An instance with an operation in progress (`deploying`, `stopping`, `cleaning`, `queued`, `stabilizing`) gives 409 (in bulk delete an `error` result with the same message), before the `pre-instance-delete` hook fires. The start of the clean is one conditional database update that also stores the delete (`stack_instances.delete_after_clean`, migration 57): of two deletes at the same time (also on two replicas) one starts the clean and the other gets 409 `Cannot delete: another operation started on the instance`. The replica that finishes the clean deletes the row. When the clean fails, the instance stays with status `error` and the message `Clean failed; the stack was not deleted. <reason>`. When the replica stops during the clean, the interrupted operation recovery sets `error` and clears the stored delete: delete the instance again. A plain clean never deletes the instance. A clean (API, bulk, cleanup policy) also starts with a conditional update: when another clean or a delete started first, it gives 409 `Cannot clean: another operation started on the instance` and does not overwrite the stored delete. The `delete` action of a cleanup policy uses the same path: a `stopped` or `error` instance is cleaned first and deleted when the clean completes; a `draft` instance is deleted at once; a running or busy instance is skipped with an error. Deleting an instance also deletes its value overrides, branch overrides, quota override and followers (all delete paths: API, bulk delete, delete after clean, cleanup policy). The followers still get the "Stack deleted" notification. Then, in a separate step, the quick deploy definition is deleted when no instance uses it any more: the definition of the deleted instance, or a definition whose owner instance no longer exists (for example a clone of a deleted quick deploy instance). An error in that step is logged and does not undo the instance delete.

**Cleanup policy conditions.** A condition is a comma-separated list of `key:value` pairs. All pairs must match.

| Condition | Meaning |
|---|---|
| `status:<status>` | The instance status is `<status>`. |
| `idle_days:N` | No deploy for N days (the creation time when the instance was never deployed). |
| `age_days:N` | The instance was created more than N days ago. This is not the time since the stop. |
| `stopped_days:N` | The instance is stopped, and the last stop finished N or more days ago. Instances without a recorded stop time never match. |
| `ttl_expired` | The expiry time is in the past. |

Use `stopped_days:N` for "stopped for N days". The stop time (`stopped_at`) is set when a stop finishes (API, TTL reaper, cleanup policy). A deploy or a clean clears it. The upgrade sets `stopped_at` for instances that are already stopped, from the last successful stop log or else from `updated_at`. Existing policies with `status:stopped,age_days:N` keep their meaning: created more than N days ago.

**Cleanup policy update.** `PUT /admin/cleanup-policies/:id` is a partial update: only the fields in the body change, for example `{"enabled": false}` or `{"dry_run": true}`. The merged policy is validated, and the scheduler reloads. The Enabled and Dry Run switches in the policy table use this.

### Custom Actions
An operator can register custom actions on the server, for example "refresh-db" or "load seed data" (see `EXTENDING.md`). A custom action runs on one stack instance.

- The instance detail page shows an **Actions** menu next to Deploy, Stop and Clean. The menu is not shown when the server has no actions.
- Every user who can see the instance sees the menu. Only the owner, an admin or a devops user can run an action. For other users the items are disabled.
- A click opens a dialog. The dialog shows the description of the action and, if the action has one, a warning text. Read the warning before you click **Run**.
- Some actions have parameters. The dialog shows a form: a text field, a switch or a list. A field marked with `*` is required.
- After the run, the dialog shows the status code and the result of the action. A status code that is not 2xx is an error.
- Some actions start a job that runs in the background. Then the dialog shows the job log and the job status (`running`, `succeeded`, `failed`). The log updates every 3 seconds until the job ends. When the server is busy or not reachable, the dialog shows "Waiting…" and tries again later. Click **Stop following** to stop the updates. The dialog stops the updates after 60 minutes and keeps the last 1 MiB of log text. Closing the dialog or stopping the updates does not stop the job; it continues on the server.
- The CLI and other API clients use the same endpoints: `GET /api/v1/stack-instances/:id/actions`, `POST /api/v1/stack-instances/:id/actions/:name` and `GET /api/v1/stack-instances/:id/actions/:name/jobs/:job_id/log?offset=N`.

### Value Override
Per-chart configuration overrides on a stack instance. Deep-merged with chart defaults during Helm values export. Template variables (`{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, etc.) are substituted at export time.

### Audit Log
Every mutating API call (POST, PUT, DELETE) is recorded with user, action, entity type, entity ID, and timestamp.

- Only devops and admin users can read the audit log (`GET /audit-logs`). Only admins can export it (`GET /audit-logs/export`). Other users get 403, and the UI does not show the Audit Log page to them.

- Plain create, update and delete calls get the action `create`, `update` or `delete` and the entity type of the resource (singular, for example `cluster`, `cleanup_policy`, `api_key`).
- Other operations get their own action, with the entity they act on. For example `POST /stack-instances/:id/deploy` gives `deploy | stack_instance | <instance id>`. The same applies to `stop`, `clean`, `rollback`, `extend_ttl`, `clone`, `invoke_action` (the action name is in the details), template `publish`, `unpublish`, `instantiate` and `clone`, definition `import` and `upgrade`, notification channel `test`, cluster `test_connection` and `set_default`, cleanup policy `run`, user `disable`, `enable`, `reset_password` and `change_role` (the old and the new role are in the details), and instance `follow` and `unfollow`. Deploy, stop, clean and rollback store the deployment log ID in the details.
- A bulk operation writes one entry for each instance or template that succeeded, with `"bulk": true` in the details.
- Marking notifications as read writes no entry. Quick deploy writes its own entry (`quick_deploy | stack_instance`).
- Filter on the entity type `stack_instance` and an instance ID to see all operations on that instance.
- A change of a nested resource keeps the parent ID as entity ID and adds the child ID to the details, for example `PUT /clusters/:id/shared-values/:valueId` gives entity ID `<cluster id>` and `{"value_id": "..."}`; a nested create adds `created_id`. A deleted orphaned namespace has the namespace name as entity ID.
- The upgrade renames the plural entity types of old create, update and delete entries to the singular names (`clusters` to `cluster`, `api_keys` to `api_key`, `quotas` to `quota`, and so on). Old operation entries keep their old values (for example `create | deploy`), because the old entry does not identify the operation reliably.

### Notifications
**In-app notifications.** Lifecycle events of a stack instance (deploy, stop, clean, rollback, delete, expiry warning, cleanup policy action) give an in-app notification to the owner of the instance and to each follower.
- Each receiver gets the notification only when the event type is on in the own notification preferences (Profile page). A receiver without a preference for the event type gets it. The Profile page lists every event type that the user can get as owner or follower (and, for admin and devops users, the system events `cleanup.policy.executed`, `quota.warning` and `secret.expiring`). The system notifications to admin and devops users also follow these preferences.
- `PUT /notifications/preferences` accepts only these event types. An unknown event type gives 400, and the request changes no preference. Notification channels accept the same event types.
- Upgrade note: before this version the backend did not store a switched-off preference (it stored "on"), and it did not check the preferences. Users who switched events off before must open the Profile page, switch them off again and save.
- An owner who also follows the instance gets one notification, not two.
- The user who started the operation is not excluded. For example, a follower who deploys the instance also gets the deploy notification. The owner always got the notifications of the own operations; followers get the same rule.
- The WebSocket message `notification.new` goes only to the sockets of each receiver.

**Follow a stack instance.** The detail page of an instance has a **Follow** button next to the favorite star. It shows the number of followers.
- Every user who can see the instance can follow it (all signed-in users). The API is `POST /stack-instances/:id/follow` and `DELETE /stack-instances/:id/follow`. Both are idempotent and return `{"following": ..., "follower_count": ...}`. An unknown instance gives 404. The audit log records `follow` and `unfollow` on the instance.
- `GET /stack-instances/:id` adds `following` (the current user follows the instance) and `follower_count`. List responses do not have these fields.
- Followers get in-app notifications only. They do not get channel (webhook) deliveries.
- A favorite does not follow the instance.
- Deleting the instance deletes its followers. Deleting a user deletes the follows of the user.

**Notification channels** (`/admin/notification-channels`, admin and devops) send events as webhooks. Each channel has event-type subscriptions and optional **filters** that limit it to some stack instances:

| Filter | Value | Example |
|---|---|---|
| `instance_name_patterns` | glob patterns for the instance name (`*`, `?`, `[a-z]`; `path.Match` syntax; case is ignored) | `rdbtest-*`, `*-se` |
| `owner_ids` | user IDs of instance owners | the team members |
| `definition_ids` | stack definition IDs | one product stack |
| `cluster_ids` | cluster IDs | the development cluster |

- Each set filter must match (AND). Inside one filter, one value is enough (OR). A channel without filters gets the events of all instances (the behaviour before filters).
- An event without an instance (quota warning, secret expiry) goes only to channels without filters.
- A cleanup policy run (`cleanup.policy.executed`) goes to a filtered channel when at least one affected instance matches.
- A channel that the filters skip gets no delivery log entry (the backend logs it at debug level).
- **Test** (`POST /admin/notification-channels/:id/test`) sends one test payload (event type `test`, one attempt, no redirect) also to a disabled channel. Each test send is in the delivery log with the event type `test`. The response has `success`, `message`, `status_code` and `channel_enabled`.
- The backend does not follow a redirect from a channel webhook. A 3xx answer is a failed delivery: the delivery log has the status code and `HTTP <code>: redirect not followed`. Delivery log messages never contain the webhook URL. Configure the final URL.
- The save checks the patterns: an invalid pattern gives 400. An owner, definition or cluster ID that does not exist gives a warning in the response (`warnings`), and the channel is saved. At most 50 values per filter.
- The channel dialog has a **Filters** section: patterns as chips (press Enter after each pattern) and pickers for owners, definitions and clusters. Admins pick owners from all users. Devops users cannot read the user list (`GET /users` is admin only), so their owner picker lists the users who own a stack now. The channel list shows "All instances" or a short summary of the filters.
- Migration 52 adds the column `notification_channels.filters` (JSON text, empty for existing channels). Migration 53 adds the table `instance_followers`.

### Cluster
A registered Kubernetes cluster that stack instances can be deployed to. Each cluster stores connection details (kubeconfig path or encrypted kubeconfig data) and is monitored via periodic health checks. One cluster can be designated as the **default** target. Clusters are managed by admins through `/admin/clusters`.

A cluster with **in-cluster configuration** (`use_in_cluster`) uses the service account of the backend pod. It has no API server URL and no kubeconfig. The admin cluster dialog has a checkbox for it; the URL is then optional, and the list shows "in-cluster".

**Cluster Health page** (`/admin/cluster-health`, admin and devops): CPU and memory are parsed as Kubernetes quantities (`1620m` of `16` is 10 %) and shown in cores and binary units (`247 GiB`). The summary cards show the sum of the pod requests of the scheduled, unfinished pods (`requested_cpu`, `requested_memory` from `GET /clusters/:id/health/summary`) against the allocatable amount, with the capacity below. A pod counts like in the kube-scheduler: the larger of its init phase and its app containers plus native sidecars, plus the pod overhead. Only this endpoint lists the pods (from the API server cache); the dashboard does not. Requests are not real use; the page does not read metrics-server. "Namespace Resource Usage" shows the quota requests (`requests.cpu`, `requests.memory` used) against the quota.

The dashboard Cluster Health widget shows only to admin and devops. The API sends the health status only to these roles (same rule as `GET /clusters`), so role `user` does not see the widget.

Clusters can optionally store **container registry credentials** (`registry_url`, `registry_username`, `registry_password`, `image_pull_secret_name`) for automatic image pull secret provisioning. When configured, a `kubernetes.io/dockerconfigjson` secret is created in each stack namespace before chart installs and refreshed periodically (every 4 hours) by a background service to handle short-lived tokens (e.g. ACR). The registry password is encrypted at rest using the same AES-GCM scheme as kubeconfig data.

### Orphaned Namespaces
`/admin/orphaned-namespaces` (admin) lists `stack-*` namespaces without a stack instance, with pod, deployment and service counts and Helm releases (`?details=true`).
- The deployer labels each namespace it creates `managed-by=k8s-stack-manager`. The page lists labelled namespaces as orphans and unlabelled `stack-*` namespaces in a separate "Unmanaged namespaces" table.
- `DELETE /admin/orphaned-namespaces/:namespace` deletes a labelled namespace. An unlabelled namespace can belong to another team or tool: the delete returns 409 unless `?confirm=<full namespace name>` is set. The page asks the admin to type the full name. A missing namespace gives 404.
- After a delete the page removes the row. The namespace can stay in phase `Terminating` for some time; a refresh then shows it with the delete button disabled.

### Analytics
`/admin/analytics` shows the overview and the template statistics to admin and devops. User activity (`GET /analytics/users`) is admin only; the page does not request it for devops. Each section loads on its own, so one failed request does not hide the others. A template without deploys shows "—" as success rate.

Per-user deploy counts and last activity come from the deploy logs: each deploy log stores the user who started it (`user_id`). They stay after the instance is deleted, and a deploy on another user's instance counts for the user who started it. Migration 49 adds an index on (`user_id`, `action`, `started_at`, `completed_at`, `status`) and sets `user_id` on older deploy logs to the owner of the instance (in batches of 1000), when the instance still exists; deploys of instances deleted before the upgrade stay without a user. "Instances" is the number of instances the user owns now.

### Users
`/admin/users` (admin) shows the sign-in method (Local or SSO) and the status (Active or Disabled) of each user. Disable (with a confirmation) and Enable call `PUT /users/:id/disable` and `/enable`. An admin cannot disable, enable or delete the own account; the buttons on the own row are disabled. Reset password shows only for local users.

Rules for delete, disable, enable and role change (guarded admin changes):

- The handler reads the user first and uses the stored ID for the self-check, the change and the revocation. MySQL compares IDs without case, so `/users/ADMIN-ID` is the own account of `admin-id`.
- The change runs in one database transaction. It locks the admin rows (ordered by ID), then the target row. Inside the lock the caller must still be an enabled admin, else 403 "Admin role required" (for example the caller was demoted or disabled after the token was issued).
- The last enabled admin cannot be demoted, disabled or deleted (409). Two admins who demote, disable or delete each other at the same time are serialized: exactly one change succeeds, the other gets 403. An index on `users.role` (migration 50) keeps the lock to the admin rows.
- Every user change writes only its own columns (role, disabled, password hash, SSO profile). No change saves the full row, so a password reset or an SSO profile sync never undoes a concurrent role change or disable. An SSO login sets the role from the identity provider only for SSO users.

Edit role (local users only) opens a dialog with a role select (`user`, `devops`, `admin`) and a warning that the user is signed out. It calls `PUT /api/v1/users/:id/role` with `{"role": "devops"}`. Rules:

- Only an admin can change a role.
- The role of an SSO user comes from the identity provider (`OIDC_ROLE_CLAIM`) and is set again at each SSO login. The API refuses the change with 409 "Role is managed by the identity provider". The page shows the role read-only with the hint "managed by SSO".
- An admin cannot change the own role (403). The button on the own row is disabled.
- The last enabled admin cannot lose the admin role (409), and the caller must still be an enabled admin (403); see the rules above.
- An unchanged role is a no-op: 200 with `"changed": false`, and the sessions of the user stay valid.
- A change signs the user out (see Revoking a User). The next request needs a new login, which gives a token with the new role. API keys stay valid and use the new role at once, because API-key auth reads the role from the database on each request.
- The change writes an audit entry `change_role | user | <user id>` with `old_role` and `new_role` in the details.

## Architecture

### Data Flow
```
Template → (instantiate) → Definition + ChartConfigs → (create instance) → Instance + ValueOverrides
```

### Storage
- **MySQL** (GORM) — the sole data store for all domain entities
- Factory in `internal/database/repository.go` initializes the repository at startup

### Authentication
- JWT-based with `Authorization: Bearer <token>` header
- Role hierarchy: `admin` > `devops` > `user`
- Admin can register users; self-registration is configurable

### Stack Instance Permissions

| Operation | Owner | `admin` | `devops` | Other `user` |
|---|---|---|---|---|
| View the instance, status, pods, deploy log, access URLs, export values, compare, clone, list actions | yes | yes | yes | yes |
| Deploy, deploy preview, stop, clean, delete, rollback, run actions, read action job logs | yes | yes | yes | no (403) |
| Edit the instance, extend the TTL | yes | yes | yes | no (403) |
| Read or change value, branch and quota overrides | yes | yes | yes | no (403) |
| Set a quota override above the cluster quota | no (403) | yes | yes | no (403) |

- A refused request returns 403 before any side effect: no hook, no status change, no deploy-log entry, no Helm call. The backend logs the refused attempt.
- The web UI hides the lifecycle buttons and shows the page read-only for a user who cannot modify the instance.
- A clone always belongs to the user who creates it.
- Quota override limit: an owner without the `admin` or `devops` role can set a quota override only at or below the cluster quota of the instance's cluster. Each value (`cpu_request`, `cpu_limit`, `memory_request`, `memory_limit`, `storage_limit`, `pod_limit`) is compared with the same cluster value as a Kubernetes quantity, so `16000m` equals `16`. A value above it gets 403, for example `cpu_limit 64 exceeds the cluster quota 16; only admin or devops can set a higher quota`. A value equal to the stored override passes, so an owner can change one field without losing an admin grant on another field; raising it or a new value above the cluster quota still gets 403. A field without a cluster value has no limit. `pod_limit: 0` means no pod limit, so it counts as above a cluster pod limit. Admin and devops can grant more. The check runs when the override is saved; a deploy does not check again.
- `PUT /stack-instances/:id/quota-overrides` replaces the whole override. A field that you do not send is cleared, and the cluster quota applies to it. To change one field, read the override with `GET`, change the field and send all fields.
- `GET /stack-instances/:id/branches/:chartId` returns the branch override of one chart (404 when there is none). The same rule as for the list applies.
- The override restriction limits who can *edit* through the override endpoints. It is not a secrecy control: the merged values (export, compare, deploy-log values) and a clone still contain the override values. Do not put secrets in value overrides; use Kubernetes Secrets or an external secret store.
- The same applies to cluster **shared values**: only admins can edit them, but every user sees them in the exported and compared values of any stack on that cluster. Do not put secrets in shared values.

### Sessions

A login (local or SSO) starts a session. The session has one refresh token at a time. Each refresh replaces (rotates) the refresh token. All refresh tokens of one session form a family. The access token carries the session ID in the `sid` claim.

| Variable | Default | Effect |
|---|---|---|
| `ACCESS_TOKEN_EXPIRATION` | `15m` | Lifetime of one access token. The client then calls `POST /api/v1/auth/refresh`. |
| `REFRESH_TOKEN_EXPIRATION` | `168h` | Upper limit for one refresh token. `SESSION_MAX_LIFETIME` usually ends the session first. |
| `SESSION_IDLE_TIMEOUT` | `30m` | The session ends when no request arrives for this time. Must be at least `ACCESS_TOKEN_EXPIRATION`. |
| `SESSION_MAX_LIFETIME` | `12h` | The session ends this long after the login, also when the user is active. Must be at least `ACCESS_TOKEN_EXPIRATION`. |
| `REFRESH_REUSE_GRACE` | `30s` | Time in which a just-rotated refresh token still gets a new access token. `0` disables the grace. Maximum `5m`. |
| `SECURE_COOKIES` | `false` | Sets the `Secure` flag on the refresh-token cookie. Set it to `true` behind HTTPS. The Helm chart sets it when the ingress has TLS. |

How the limits work together:

- Idle time counts from the last authenticated request, not from the last refresh. Each request with a session access token updates the session activity (at most one database write per minute per session). API-key requests do not change sessions.
- A refresh never extends the session. A new refresh token expires at the earlier of `now + REFRESH_TOKEN_EXPIRATION` and `login time + SESSION_MAX_LIFETIME`.
- After `SESSION_MAX_LIFETIME` the user must log in again. An SSO login reads the role and the account state from the identity provider again. So a role change in the identity provider applies at the latest after `SESSION_MAX_LIFETIME` plus one `ACCESS_TOKEN_EXPIRATION`.
- Two browser tabs can refresh at the same time with the same cookie. The second request presents a token that the first request just rotated. Inside `REFRESH_REUSE_GRACE` it gets a new access token and no new cookie. Nothing is revoked.
- Any other reuse of a used refresh token revokes the session family (replay protection). Other sessions of the user stay active. A token revoked by logout never gets the grace.
- An access token stays valid until it expires, also when the session idles out. Revocation (see below) uses the token blocklist instead.

Note: An admin can change the role of a local user (`PUT /api/v1/users/:id/role`, see Users). The change revokes the sessions of the user, so it applies to the next request. For SSO users the identity provider controls the role; the role syncs at each SSO login.

Exceptions to the session limits:

- **CLI token from the SSO CLI login** (`stackctl login` through the browser): a long-lived access token (`JWT_EXPIRATION`, default 24h) without a session. `SESSION_IDLE_TIMEOUT` and `SESSION_MAX_LIFETIME` do not apply. It keeps the role it had at the login until it expires. Revoking the user (table below) still rejects it.
- **API keys** have no session. They use the user's current role in the database and stay valid until they expire or are revoked.
- **WebSocket** traffic (`/ws`) does not count as activity for the idle limit. A tab that only receives live updates idles out after `SESSION_IDLE_TIMEOUT`. The server closes a socket when its access token expires; the web UI reconnects with the current token.
- **Lost refresh response:** if the server rotates the refresh token but the browser never gets the response (network drop, client timeout), the browser keeps the used cookie. A retry within `REFRESH_REUSE_GRACE` still works, but sets no new cookie. The next refresh after the grace window counts as a replay and ends the session; the user must log in again. This fails safe by design.

### Revoking a User

| Action | Access tokens issued before | Refresh tokens | API keys | Open WebSocket connections |
|---|---|---|---|---|
| Delete the user | rejected (401) | revoked | deleted | closed |
| Disable the user | rejected (401) | revoked | kept, but rejected while the user is disabled | closed |
| Reset the password | rejected (401) | revoked | kept | closed |
| Change the role | rejected (401) | revoked | kept (they use the new role at once) | closed |
| Log out (`/auth/logout`) | the current token is rejected (401) | the presented token is revoked | kept | the sockets of the current token are closed |
| Log out of all sessions (`/auth/logout-all`) | the current token is rejected (401) | all revoked | kept | all sockets of the user are closed |

A new login after a password reset or a role change works at once, also in the same second. The login cache (`LOGIN_CACHE_TTL`) only skips the password check. After the password check the login reads the user again and uses that record for the disabled check and the token role; a password changed during the login gives 401. Only tokens issued before the action are rejected. Access tokens have an `iat_ms` claim (issue time in milliseconds), and the user block stores its time in milliseconds, so the check has millisecond precision. A token without `iat_ms` (issued by an older version) is rejected when it was issued in the second of the block or before it. The block time and the token issue time come from the clocks of different replicas, so the replicas need synchronized clocks (NTP). During a rolling update with replicas of different versions, two blocks of the same user in the same second use the millisecond time of the block from the newer version. Enabling a disabled user does not bring back the tokens issued before the disable.

WebSocket connections (`/ws`):

- The upgrade runs the same checks as the HTTP API. A revoked token, a token issued at or before a user block, a deleted user and a disabled user get 401. If the session store fails, the check passes and the backend logs the error (same policy as the HTTP API).
- The server closes an open socket (close code 1008) when the user is revoked or the session logs out (table above), and when the access token of the socket expires. The web UI then reconnects. It refreshes the token first when the server closed the socket with 1008 or the token expires within 30 seconds, and stops when the refresh is rejected. The refresh does not count as activity for `SESSION_IDLE_TIMEOUT`.
- Every minute each backend replica checks its open sockets again (token blocklist, user blocklist, deleted or disabled user) and closes the revoked ones. If the session store or the database fails, the check passes and the backend logs the error.
- After `/auth/logout-all`, the other sessions of the user keep a valid access token until it expires, so their sockets can reconnect until then.
- With more than one backend replica and WebSocket fan-out on (`WS_FANOUT_ENABLED=true`, the Helm default), a revoke closes the sockets on all replicas within about half a second. The replica that handles the revoke request closes its own sockets at once. Without fan-out, the other replicas close them at the next check, within about one minute, or at token expiry if that is earlier. The check stays on as a fallback in both cases. It also covers long-lived tokens (`JWT_EXPIRATION`, for example the CLI token and the mode without refresh tokens).

### Multi-Cluster
- Clusters are registered via the API with a kubeconfig path or kubeconfig data (encrypted at rest with AES-GCM)
- `ClusterRegistry` manages per-cluster Kubernetes and Helm clients
- A health poller periodically checks cluster connectivity and updates status
- Stack instances target a specific cluster (or the default cluster)
- The `deployer` package routes deploy/undeploy/status operations through the registry to the correct cluster
- Per-cluster container registry credentials enable automatic image pull secret provisioning; a `SecretRefresher` runs every 4 hours to keep tokens current

### Backend Replicas
- Every backend replica serves the API and WebSocket clients.
- Only one replica, the leader, runs the background jobs: TTL reaper, expiry warning, cleanup policies, quota and secret warnings, pull secret refresh, cluster health checks and the k8s status watcher. So each job runs once, also with 2 or more replicas.
- The replicas elect the leader with a Kubernetes Lease (`LEADER_ELECTION_ENABLED=true`, on in the Helm chart). When the leader stops or loses the lease, another replica takes over after a few seconds (about 2 seconds on a normal shutdown, at most about 15 seconds after a crash).
- Without election (`LEADER_ELECTION_ENABLED=false`, the default for docker-compose and local development), every process runs the jobs. Use one replica then.
- A cleanup policy change takes effect on the leader within one minute. A scheduled policy run that fell into a leader change runs when the new leader starts (when it was due in the last 5 minutes).
- After a leader change, one quota or secret warning can come again (the cooldown of these warnings is in memory).
- Every replica writes a heartbeat to the database every 30 seconds. The leader ends the deploys, rollbacks, stops and cleans of a replica without a heartbeat for 2 minutes (see Interrupted operation above).
- Metric `stackmanager_leader` is 1 on the leader. See [ARCHITECTURE.md](ARCHITECTURE.md#replica-model).
- The replicas share live updates (deploy logs, status changes, cluster health, notifications, socket revocations) through the database table `ws_events` (`WS_FANOUT_ENABLED=true`, on in the Helm chart). A client on any replica sees the events of all replicas. Events from another replica arrive up to about half a second later (`WS_FANOUT_POLL_INTERVAL`, 500ms).
- Without fan-out (`WS_FANOUT_ENABLED=false`, the default for docker-compose and local development), a client sees only the events of its own replica. Use one replica then.
- A message larger than 512 KiB, or a message sent while the fan-out buffer is full, reaches only the clients of the replica that sent it. After a database outage, events older than 30 seconds are not delivered. The page shows the current state after a reload.
- An in-app notification goes only to the sockets of the notified user.

### Git Integration
- Auto-detects provider from repository URL (`dev.azure.com` → Azure DevOps, `gitlab.com` → GitLab)
- Branch listing with in-memory caching (5-minute TTL)
- Service-level tokens (PAT/token), not per-user

### Helm Values
- Deep merge, lowest first: cluster shared values (by priority, then name in byte order, then ID) ← chart defaults ← instance overrides ← template locked values (locked always wins)
- Template variable substitution: `{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}`, `{{.Owner}}`
- The same merge feeds deploy, deploy preview, bulk deploy, quick deploy, export and compare
- Export: `GET /stack-instances/:id/values` (ZIP, one `values.yaml` per chart) or `GET /stack-instances/:id/values/:chartId` (YAML)

### Branding
The frontend image is the same for every installation. The product name, the logo and the favicon come from the backend at runtime.

| Variable | Default | Effect |
|---|---|---|
| `APP_TITLE` | `K8s Stack Manager` | Name in the browser tab, sidebar, app bar, login page and setup wizard. Maximum 100 characters. |
| `APP_LOGO_URL` | empty | Logo image. Empty: the built-in logo (`/logo.svg`). |
| `APP_FAVICON_URL` | `/favicon.svg` | Browser tab icon (built-in). |

- `GET /api/v1/ui-config` returns `{"title", "logo_url", "favicon_url"}`. It needs no token (the login page uses it), has the API rate limit and `Cache-Control: public, max-age=300`.
- The web UI renders at once with the last config it received (browser localStorage key `ui-config`) or, on the first visit, the built-in values. It loads the config once at startup, swaps in its values when they arrive (also on the login page) and stores them for the next start. If the call fails or takes more than 5 seconds, the initial values stay. A logo that does not load falls back to the built-in logo.
- URL rule: a same-origin path (`/branding/logo.svg`) or an `https://` URL with a host. The backend refuses other values at startup with a clear error: `http://` (mixed content, clear text), `javascript:` and other schemes, all `data:` URLs (an SVG data URL can carry script), protocol-relative URLs (`//host/x`), paths without a leading `/`, user info, spaces and control characters. A path resolves against the frontend origin, not the API.
- Helm: `branding.title`, `branding.logoUrl` and `branding.faviconUrl` set the variables (an explicit `backend.env` value wins). `branding.files` (text, for example SVG) and `branding.binaryFiles` (base64, for example PNG or ICO) put the files in a ConfigMap (file names `^[-._a-zA-Z0-9]+$`, at most 900 KiB in total, else the render fails) that the frontend nginx serves at `/branding/<name>`, with the content type of the extension, `X-Content-Type-Options: nosniff` and a sandbox Content Security Policy. Example:

```yaml
branding:
  title: "Platform Portal"
  logoUrl: /branding/logo.svg
  faviconUrl: /branding/logo.svg
  files:
    logo.svg: |
      <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">...</svg>
```

## Development

### Prerequisites
- Docker and Docker Compose
- Go 1.26+ (backend)
- Node.js 22+ (frontend)

### Running
```bash
make dev              # Full stack via Docker Compose
make dev-local        # Backend only, local
```

### Testing
```bash
make test             # All unit tests
make test-backend-all # Backend unit + integration (starts MySQL)
make test-e2e         # End-to-end with Playwright
```

### Adding a New API Resource
See `.github/instructions/api-extension.instructions.md` for the step-by-step guide.

## Troubleshooting

- **JWT errors**: Ensure `JWT_SECRET` is set and at least 16 characters.
- **Database connection errors (MySQL)**: Ensure MySQL is running. Check `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD` environment variables.
- **Git provider errors**: Check `AZURE_DEVOPS_PAT` (or `AZURE_DEVOPS_AUTH=workload-identity` with the `AZURE_*` variables) or `GITLAB_TOKEN` are set correctly. Empty tokens are valid (provider just won't be available).
- **Cluster connection errors**: Verify the kubeconfig path or data is valid. Use the "Test Connection" button on the Clusters admin page. If `KUBECONFIG_ENCRYPTION_KEY` is set, all kubeconfig data is encrypted at rest — changing the key will make existing encrypted data unreadable.
