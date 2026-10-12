# K8s Stack Manager: Wiki

This page explains the concepts and the rules of K8s Stack Manager. For the setup, see the [README](README.md) and the [Getting Started guide](docs/getting-started.md). For the system design, see [ARCHITECTURE.md](ARCHITECTURE.md).

Terms in this document:

- A **template** is a reusable blueprint of Helm charts.
- A **stack definition** (or **definition**) is a concrete set of Helm chart configurations.
- A **chart config** is the configuration of one Helm chart in a definition or a template.
- A **stack instance** (or **instance**) is a deployed copy of a definition.
- A **cluster** is a registered Kubernetes cluster.
- The roles are `user`, `devops`, and `admin`.

## Concepts

### Stack Template

A template is a reusable blueprint. A user with the `devops` role creates it. A template has a set of Helm chart configurations with default values. A user can **publish** a template for other users. A template author can **lock** the value of a chart, so that nobody can change it.

**Clone.** `POST /templates/:id/clone` (`devops` and `admin`) copies the template and its charts into a new draft that belongs to the caller.

- The optional body `{"name": "..."}` sets the name. The API trims the name. An empty name gives 400.
- Without a name, the clone is called `<source name> (Copy)`.
- Template names do not have to be unique.

### Template Life Cycle (Draft and Release)

A template has a **working copy** and a **version history**.

- **Working copy (draft).** The working copy is the template fields and its charts. A `devops` user edits the working copy, also while the template is published. An edit never changes what users get. `PUT /templates/:id` is a partial update: only the fields in the body change.
- **Who can change a template.** Edit, delete, publish, unpublish, and chart changes need the `devops` role and either the template owner or an `admin`. This is the same rule as for the bulk operations. Other users get 403.
- **Scripts.** A script that changes a template (for example `stackctl template update-chart`) changes only the working copy. Publish a new version afterwards with `POST /templates/:id/publish` and a new `version`. Otherwise users keep getting the old content.
- **Publish.** `POST /templates/:id/publish` stores the working copy as a **snapshot** (a version). The optional body is `{"version": "x.y.z", "change_summary": "..."}`. Without `version`, the API uses the version of the working copy. The working copy then takes the published version.
  - A version string is unique for each template. A version that exists gives 409 `Version x already exists`. Set a new version to publish changes.
  - When the working copy equals the latest snapshot (same content and same version), publish creates no snapshot and returns 200 with `snapshot_created: false`. Publish is idempotent.
  - Bulk publish and create with `is_published: true` follow the same rules.
- **What users get.** Use Template, Quick Deploy, the definition upgrade (check-upgrade and upgrade), and the locked values of the template at deploy time read the **latest snapshot**. They never read the working copy.
- **Unpublish.** `POST /templates/:id/unpublish` hides the template. Use Template and Quick Deploy then return 409 `Template has no published version`, and upgrades are not offered. The history stays. A template that was never published also gives 409, for the owner too.
- **Template detail.** `GET /templates/:id` returns the template fields and these fields: `published_version`, `published_version_id`, `published_charts` (the snapshot charts; null without a snapshot), and `has_unpublished_changes`.
  - Only the owner and `admin` users get the working copy in `charts`.
  - For other users, `charts` equals `published_charts` (an empty list without a snapshot). `version` is the published version (empty without a snapshot). `has_unpublished_changes` is false. So the draft stays hidden.
- **Find by name.** `GET /templates?name=<name>` filters by exact name. The response keeps the paged envelope. stackctl uses it to resolve template names.
- **Unpublished changes.** `GET /templates/:id/versions/diff?left=<version id>&right=working` compares a snapshot with the working copy. Either side can be `working`. Only the owner and `admin` users can compare the working copy. Others get 403.
- **Old-format snapshots.** A latest snapshot in the old format (no chart version and no chart path) always counts as changed. The next publish stores the full format. It can use the version string of that snapshot once.
- **Use Template overrides.** `chart_overrides` replace the chart default values in the new definition. The keys are `published_charts[].id`, the `charts[].id` of the working copy (the API maps them by chart name), or chart names. The API ignores keys for charts that are not in the snapshot.
- **Upgrade.** Check-upgrade returns `chart_diffs` (the same shape as the version diff). The left side is the definition now. The right side is the latest snapshot.
- **Existing data.** The upgrade migration stores the working copy as a new snapshot for some templates. These are the templates that are published or that a definition uses. The migration does this when the latest snapshot is missing (published templates only), in the old format, or different from the working copy. So users keep the content that they got before the upgrade.
  - These snapshots have the summary `Backfill: release of the working copy at upgrade`. They can repeat an existing version string.
  - A template without a version gets `1.0.0` (snapshot and working copy).
  - Duplicate versions from older releases stay in the history.
- **Value comparison.** Publish, `has_unpublished_changes`, and the diffs ignore trailing spaces and line breaks in values.

### Stack Definition

A definition is a concrete collection of Helm chart configurations. You create it by instantiating a template, or from scratch. It owns the chart configs (chart name, repository, version, default values).

- **Chart name.** The chart name is also the Helm release name. It has at most 53 characters. It can contain lowercase letters, digits, `-`, `.`, and `_`, and it must start and end with a letter or digit.
- **Who can create and change.** Only `devops` and `admin` users can create or import a definition, and add or remove charts. Users get definitions from templates (Use Template, Quick Deploy).
  - The owner can rename a definition, change its branch, change the default values of its charts, and change the order of its charts.
  - Changes to the chart name, repository, path, or version need `devops` or `admin` (403 for others).
  - Only the owner, `devops`, and `admin` can change or delete a definition (403 for others).
  - Only `devops` and `admin` users can export a definition.
- **Chart sources.** When `ALLOWED_CHART_REPOS` is set, the chart source must be in an allowed repository. See [Chart sources](#chart-sources).
- **Unique names.** Definition names are unique for each owner. Create, rename, and template instantiate return 409 for a name that the owner already uses.
- **Import.** Import does not fail on a used name. It uses `<name> (imported)`, then `<name> (imported 2)`, and so on. The response shows the final name.
- **Quick deploy definition.** Quick deploy creates one definition for the new instance and sets `owner_instance_id`. When you delete that instance, the API also deletes the definition and its charts, if no other instance uses the definition. If the name is used, quick deploy names the definition `<name> (2)`, `<name> (3)`, and so on.
- **Edits do not keep a quick deploy definition.** You can edit it, for example to add a chart. The API still deletes it with its owner instance when no other instance uses it. To keep a setup, ask a `devops` or `admin` user to create a definition (for example with export and import).
- **Older data.** Older databases can contain duplicate names. They stay until you rename or delete them.
- **Case.** On MySQL the name check is not case-sensitive (the default collation): `My-Def` and `my-def` are the same name. The in-memory test repositories compare exactly.
- **List.** The paged list (`GET /stack-definitions`) does not load the charts. It returns `chart_count` (one grouped count query for the page). The definitions page shows it in the Charts column.

### Chart sources

Helm installs each chart with the identity of the backend. A chart config decides which chart Helm installs. `ALLOWED_CHART_REPOS` (Helm value `backend.allowedChartRepos`) limits the chart sources.

- The value is a comma-separated list of URL prefixes. Each prefix has the scheme `oci`, `https`, or `http` and a host, for example `oci://registry.example.com/charts`. A prefix cannot contain whitespace, `?`, `#`, `@`, `\`, or `..`. An invalid value stops the backend at startup.
- An empty list allows every chart source. This is the default. Set the list on every cluster that untrusted users can deploy to.
- The backend builds the chart location from the repository URL and the chart path (or the chart name). The location must equal a prefix or start with a prefix and `/`.
- A chart path with a scheme or a leading `/` next to a repository URL is rejected. A location with `..`, `\`, or whitespace is rejected. A chart path without a repository URL (a local path or a repository alias) is rejected, unless it starts with an allowed prefix.
- The backend checks the chart configs of definitions and templates when they are saved. This includes import, instantiate, quick deploy, and upgrade. A chart that is not allowed gives 400.
- The backend checks again at deploy time and at rollback time. A chart config that was saved before the list was set does not deploy. The deploy log shows an `ERROR:` line for the chart, and the chart counts as failed.

### Backend rights on the cluster

The Helm chart gives the backend service account its rights. These rights apply to a cluster that is registered with `use_in_cluster`. For a cluster that you register with a kubeconfig, the rights of the kubeconfig apply.

`backend.rbac.scope: namespace` is the default:

- In each stack namespace, a RoleBinding `k8s-stack-manager-access` binds the backend service account to the ClusterRole `<fullname>-stack-access`. This role allows all verbs on all resources, but only inside that namespace. A stack chart can therefore not read or change objects in other namespaces, and cannot create cluster-scoped objects (ClusterRoles, CRDs, webhooks).
- The backend creates the RoleBinding when it creates or checks the namespace, before any other object. The leader replica also adds it to existing namespaces with the label `managed-by=k8s-stack-manager`. The worker `stack-access-reconcile` runs at the start of each leader term and then every 10 minutes.
- The backend keeps these cluster-wide rights: read nodes, manage namespaces, read pods (for the cluster overview), write RoleBindings, and `bind` on the stack-access ClusterRole and on the ClusterRoles of `backend.namespaceRoleBindings`.
- When `WILDCARD_TLS_SOURCE_NAMESPACE` and `WILDCARD_TLS_SOURCE_SECRET` are set, a Role gives read access to that one Secret.
- The chart sets `STACK_ACCESS_CLUSTER_ROLE` and `STACK_ACCESS_SERVICE_ACCOUNT` (`<namespace>/<name>`). Both must be set, or neither.

`backend.rbac.admissionPolicy.enabled` (default `true`) adds a ValidatingAdmissionPolicy. It needs Kubernetes 1.30 or later. The policy applies only to the backend service account:

- The backend can create, change, or delete Roles and RoleBindings only in namespaces with the label `managed-by=k8s-stack-manager`.
- The backend can create only namespaces with that label.
- The backend can change or delete only namespaces whose name starts with `stack-` or that have the label.

Without the policy, the cluster-wide RoleBinding right could give the backend access to any namespace.

`backend.rbac.scope: cluster` keeps the rules of chart 0.6 and earlier. The backend has rights on namespaced objects in all namespaces. Use it only when stack charts need cluster-scoped objects.

### Stack Instance

An instance is a developer's working copy of a definition. Each instance has:

- An **owner** (the developer).
- A **branch** (the Git branch for the deployment).
- **Value overrides** for each chart. The application merges them on top of the chart defaults.
- An automatic **namespace** (`stack-{instance-name}-{owner}`).

The instance **status** is one of `draft`, `queued`, `deploying`, `stabilizing`, `running`, `stopping`, `stopped`, `cleaning`, `partial`, or `error`.

**Namespace labels.** The backend labels each stack namespace `managed-by=k8s-stack-manager` and `k8s-stack-manager.io/instance-name=<instance name>`.

- The instance-name label gives the exact stack name. Use it, for example, in an admission policy that binds Ingress host names to the stack. A prefix of the namespace name is ambiguous.
- A rename changes the label at the next deploy.
- An instance name that is not a valid label value (older names from before the naming rule) gets no instance-name label. The backend logs a warning.
- `NAMESPACE_LABELS` (Helm: `backend.namespaceLabels`) adds more labels, for example Pod Security Admission levels: `NAMESPACE_LABELS=pod-security.kubernetes.io/enforce=baseline,pod-security.kubernetes.io/warn=restricted`.
- The backend sets these labels when it creates the namespace. On each deploy and on each rollback to a target deploy, it adds a missing label to an existing namespace. It also sets the configured value when the live value is different. It does not remove other labels.
- An invalid key or value, a reserved key (`managed-by`, `k8s-stack-manager.io/instance-name`), or a duplicate key stops the backend at startup.

**List filters.** `GET /stack-instances` accepts these filters. They combine (AND). `total` is the number of instances that match.

- `status`: one of the status values above. Another value gives 400.
- `cluster_id`, `definition_id`: an ID.
- `owner`: `me` (you), a username, or a user ID. A value in UUID form is matched as a user ID first, then as a username. An unknown owner gives an empty list.
- `name`: the exact instance name.

`GET /stack-definitions` accepts `owner` and `name` in the same way.

Both list endpoints are paged (`page` and `pageSize`; default 25, maximum 100). With `owner=me` or `name`, `total` is the real number of matches, not the page size.

**Names in responses.** Instance responses include `owner_username`, `definition_name`, and `cluster_name`. Definition and template responses include `owner_username`. The API omits a field when the owner, definition, or cluster no longer exists (for example after a delete). It also omits `cluster_name` when `cluster_id` is empty (older instances).

**Access URLs.** The instance status (`GET /stack-instances/:id/status`) lists the URLs of all Ingresses in the namespace. A chart marks the main entry point of the stack (for example the web application, not a database or mail tool) with the Ingress annotation `k8s-stack-manager.io/primary-url: "true"`. Primary Ingresses come first in `ingresses` (field `primary: true`), and the stack list shows the first URL. Without the annotation, the order is the Ingress name order.

### Stack Instance Lifecycle

**Names.** An instance name must be a DNS label (RFC 1123). It has lowercase letters `a-z`, digits `0-9`, and `-`. It must start and end with a letter or a digit. The maximum length is 50 characters.

- Charts build host names from `{{.InstanceName}}`, so other characters break the Ingress.
- The API checks the rule on create, clone, quick deploy, and rename (400).
- Older instances with other names keep working. An update that keeps the name does not fail.

**Clone.** `POST /stack-instances/:id/clone` accepts an optional body `{"name": "...", "branch": "...", "ttl_minutes": N}`.

- Without a name, the API uses the first free name of `<name>-copy`, `<name>-copy-2`, and so on.
- The clone gets the cluster, the TTL, the value overrides, and the branch overrides of the source.
- The clone gets the quota override only when the caller owns the source or is `admin` or `devops`. Any other user gets the cluster quota. A quota override is a resource grant, not configuration.
- An owner without the `admin` or `devops` role gets the quota override only when it stays within the cluster quota (the same rule as for setting a quota override, see below). Otherwise the clone uses the cluster quota. The response has a `warning` field that says so, and the backend logs the skip. The clone still succeeds.
- `branch` and `ttl_minutes` replace the values of the source. The clone is a draft and belongs to the caller.

**Extend.** `POST /stack-instances/:id/extend` never makes the expiry earlier and never changes `ttl_minutes`.

- `{"minutes": N}` adds N minutes to the current expiry. When the instance already expired or has no expiry time, it adds N minutes to now. N must be greater than 0.
- An empty body adds the TTL of the instance (`ttl_minutes`) in the same way.
- The new expiry is at most now + 30 days.
- Deprecated: a body with only `ttl_minutes` keeps the old behavior (expiry = now + `ttl_minutes`, and `ttl_minutes` changes). stackctl 0.4.0 and earlier send this body. The response has a `Warning` header.
- The response is the instance with the new `expires_at`.

**Denied deploy.** A `pre-deploy` hook with `failure_policy: fail` can deny a deploy. The instance then gets the status `error`.

- The reason is `pre-deploy hook "<name>" denied the deployment: <subscriber message>`.
- The reason is in the `error_message` of the instance (shown on the detail page) and in the `error_message` of the deploy log.
- The reason is also an `ERROR:` line in the deploy log output (`stackctl stack logs`).
- The hook progress lines (`LOG:`) stay in the output.
- A hook that cannot be reached gives `pre-deploy hook "<name>" failed (unreachable or timed out)`.
- The API never shows the subscriber URL.

**Pod problems in the deploy log.** The backend reads the Warning events and the container states of the namespace every 10 seconds. It does this while Helm installs the charts and while the pods stabilize (deploy and rollback).

- Each new problem gives one `Pod event: <reason> <object>: <message>` line in the deploy log (live and stored).
- The problems include pods that cannot be created (`FailedCreate`, for example `exceeded quota` of the ResourceQuota, or a LimitRange `forbidden`), `FailedScheduling`, `FailedMount`, image pull errors (`ErrImagePull`, `ImagePullBackOff`), `CrashLoopBackOff`, and similar problems.
- A repeated event is shown once. Problems with the same reason and object form one group with a count, for example `Pod event: FailedCreate replicaset/db (x18): <latest message>` when the quota rejects the pods of one ReplicaSet. Each rejected pod gives an event with another pod name.
- The count is the number of distinct events, not the number of rejected pods. Kubernetes merges similar events into one event with a count, and such a merged event counts once.
- The live log shows a group when it starts and again each time its count doubles. The stored log shows the group with its count once for each chart install and once at the end.
- The backend reports at most 5 new groups for each poll and 30 for each operation.
- When the operation fails, the backend adds the most relevant problem (a quota or LimitRange rejection first) to the error message: `... (pod event: FailedCreate job/<name>: ... exceeded quota: ...)`. A readiness timeout warning gets it too.
- The events count from the start of the operation. The container states are the current state of the pods. So a container that was in `CrashLoopBackOff` before the operation also shows.
- Stop and clean do not wait for pods and do not read the events.

**Resource quota order.** A deploy applies the namespace ResourceQuota and LimitRange right after it creates or checks the namespace. This is before the first Helm install. The quota is the cluster quota merged with the instance quota override. A redeploy applies the current quota in the same place.

- So every pod of the deploy meets the quota at once. A pod that the quota rejects shows as a pod event. The deploy ends `partial` or `error` with the quota message.
- A rollback to a target deploy (`target_log_id`, which can create the namespace again) does the same before its first install.
- If the backend cannot prepare the namespace before the installs, Helm creates it and the backend applies the quota after the installs.
- A failed quota apply gives a `WARNING` line in the deploy log and does not stop the deploy. The deploy tries once more after the installs.
- Warning: A quota that you lower below the current use of the namespace refuses new pods, also the surge pods of a rolling update. The Helm wait for that chart then fails (timeout), and the pod events show the quota message. Raise the quota or reduce the requests before you redeploy.

**Per-container limits.** The cluster quota and the instance quota override have per-container fields for the namespace LimitRange (`stack-manager-limits`):

| Field | Meaning |
|---|---|
| `container_cpu_request`, `container_memory_request` | Default request of a container |
| `container_cpu_limit`, `container_memory_limit` | Default limit of a container |
| `container_cpu_max`, `container_memory_max` | Maximum limit of one container |
| `container_ephemeral_storage_request`, `container_ephemeral_storage_limit` | Default ephemeral storage request and limit of a container |
| `ephemeral_storage_limit` | Sets `requests.ephemeral-storage` and `limits.ephemeral-storage` of the namespace ResourceQuota |

- A container without its own requests or limits gets the defaults.
- The namespace totals (`cpu_request`, `cpu_limit`, and so on) are never copied into a container default.
- If a resource has a namespace total or a container maximum but no container default, the LimitRange gets a fallback default: CPU 100m request and 500m limit, memory 128Mi request and 512Mi limit, ephemeral storage 128Mi request and 1Gi limit. Each fallback is capped at the namespace total and the container maximum (the smaller value wins).
- A ResourceQuota on CPU, memory, or ephemeral storage refuses a container without requests and limits. So the namespace needs these defaults.
- The LimitRange has a maximum only when you set it.
- Rules (400): each value is a Kubernetes quantity. For each container, request <= limit <= maximum. A container default is not above the namespace total of the same resource (the maximum can be above it).
- The override merge works for each field, as for the other fields.

**Deploy log status.** A deploy log has the status `running`, `success`, `error`, or `partial`.

- `partial` means that some charts deployed and others failed. The instance status is `partial` too.
- Older deploy logs keep `error` for a partial deploy (no data migration).
- A `partial` deploy is not a rollback target. Only `success` is.
- The deploy statistics (analytics) count a `partial` deploy as an error.
- Stop, clean, and rollback logs use `running`, `success`, and `error` only.
- The backend needs `list` on `events` and `get` and `list` on `pods` in the stack namespace. Without these rights, the log has no pod event lines, and the backend logs one warning for each operation.

**Rollback.** `POST /stack-instances/:id/rollback`:

- Without a body, each Helm release goes back one revision.
- With `{"target_log_id": "<log id>"}`, each chart gets the values of that deploy again (`helm upgrade --install` with the stored values snapshot).
  - The chart version is the version that the deploy recorded. Older deploy logs have no recorded versions. Then the current chart version is used.
  - A deploy with an empty chart version records no version. So the rollback installs the newest chart.
  - Charts that the target deploy did not include do not change.
- A rollback to a target restores the stored **values** (including the shared and locked values of that time). It does not restore the images. Image tags that are branch names can point to newer images now.
- Pre-deploy hooks do not run for a rollback. Subscribe image gates to `pre-rollback`. It gets the same chart list as `pre-deploy` (name, version, branch, `image_tag`) and the metadata `rollback_mode`, `target_log_id`, and `target_branch` (see [backend/docs/hooks.md](backend/docs/hooks.md)).
- In both modes the backend clears a release that is stuck in `pending-*` before the upgrade or rollback (as for a deploy). The instance waits for pod readiness (`stabilizing`) when readiness gating is on.
- The `pre-rollback` hook runs in the background after the API answered 202. If the hook rejects the rollback, the rollback log ends with the status `error` and the reason, and the instance gets its previous status back.
- A one-revision rollback that fails on a chart records the values of the charts that it already rolled back, so the deploy preview stays correct.
- The target must be a successful deploy of the same instance. Another log returns 400. An unknown log or a log of another instance returns 404.
- The rollback does not change the stored value overrides or branch overrides. After a rollback, the deploy preview compares against the values that now run (a failed rollback records the charts that it already upgraded).
- `values_drift: true` (in the rollback response for a target, in the deploy preview, and in `GET /stack-instances/:id`) means that the next deploy applies the stored overrides again and undoes the rollback.
- Each deploy log and rollback log records the branch (`branch`). A rollback to a target records the branch of the target deploy.

**Interrupted operation.** The replica that runs a deploy, rollback, stop, or clean can stop suddenly (for example an out-of-memory kill or a node failure). The instance then stays `deploying`, `stabilizing`, `stopping`, or `cleaning` for some time.

- The backend then sets the instance to `error` with the message `Interrupted: the server that ran this operation stopped. Deploy again.` For a stop the message ends with `Stop again.`. For a clean it ends with `Clean again.`.
- The deploy log also ends with this error. The owner and the followers get the failure notification.
- This happens when the operation passed its deadline and the replica sent no heartbeat for 2 minutes. The deadline is the longest time that the operation can take (hook timeouts, one deploy timeout for each chart, the readiness wait, and the blocking `post-deploy` hooks), plus 5 minutes.
- When the slow replica finishes later, it does not change the result. An operation on a running replica is never changed.
- To end the state earlier, use Stop.
- A normal restart (rolling update) ends the running operations at once with an error.

**Expiry warning.** About 30 minutes before the expiry, the owner and the followers get one "Stack expiring soon" notification. The backend records the warning in the database (`expiry_warned_at`), so a restart or a second backend replica does not send it again. A new expiry time (deploy, extend, TTL change) allows a new warning.

**Expiry.** When the TTL reaper stops an expired instance, the owner and the followers get one "Stack expired" notification (`stack.expired`; notification channels can subscribe to it). The stop then gives the usual "Stack stopped" (or "Stop failed") notification.

**TTL.** An instance can have a time to live (`ttl_minutes`). The maximum is 43200 minutes (30 days). A new instance without a TTL gets `DEFAULT_INSTANCE_TTL_MINUTES` when you set it (default `0`, which means no TTL). A deploy sets the expiry (`expires_at`) to now + `ttl_minutes` (see Redeploy below for a running instance).

**Redeploy.** `POST /stack-instances/:id/deploy` also works for a `running` instance. It upgrades the releases with the current values. With a TTL, the expiry becomes now + `ttl_minutes`, unless the current expiry is later. A redeploy never makes the expiry earlier.

**Delete.** `DELETE /stack-instances/:id` and bulk delete (`POST /stack-instances/bulk/delete`) apply the same rules to each instance.

- An instance with cluster resources (`running`, `partial`, `stopped`, `error`) is cleaned first (Helm uninstall and namespace delete). The row is deleted when the clean completes.
  - The single delete gives 202 with the `log_id` of the clean.
  - Bulk delete gives a `success` result with the `log_id`. Note: This `success` means that the clean started.
  - When the clean finishes, the backend removes the row. Then the WebSocket message `instance.deleted` (`instance_id`, `log_id`) goes to all clients.
- A new instance (create, clone, quick deploy) sends `instance.created` (`instance_id`) to all clients. So an open stack list shows instances that other users create.
- A `draft` instance is deleted at once (204). `instance.deleted` (`instance_id`) goes to all clients.
- An instance with an operation in progress (`deploying`, `stopping`, `cleaning`, `queued`, `stabilizing`) gives 409 (in bulk delete an `error` result with the same message). This check runs before the `pre-instance-delete` hook fires.
- The start of the clean is one conditional database update. It also stores the delete (`stack_instances.delete_after_clean`, migration 57).
  - Of two deletes at the same time (also on two replicas), one starts the clean. The other gets 409 `Cannot delete: another operation started on the instance`.
  - The replica that finishes the clean deletes the row.
  - When the clean fails, the instance stays with the status `error` and the message `Clean failed; the stack was not deleted. <reason>`.
  - When the replica stops during the clean, the interrupted operation recovery sets `error` and clears the stored delete. Delete the instance again.
- A plain clean never deletes the instance. A clean (API, bulk, cleanup policy) also starts with a conditional update. When another clean or a delete started first, it gives 409 `Cannot clean: another operation started on the instance` and does not overwrite the stored delete.
- The `delete` action of a cleanup policy uses the same path. A `stopped` or `error` instance is cleaned first and deleted when the clean completes. A `draft` instance is deleted at once. A running or busy instance is skipped with an error.
- Deleting an instance also deletes its value overrides, branch overrides, quota override, and followers (all delete paths: API, bulk delete, delete after clean, cleanup policy). The followers still get the "Stack deleted" notification.
- Then, in a separate step, the backend deletes the quick deploy definition when no instance uses it any more. This applies to the definition of the deleted instance, and to a definition whose owner instance no longer exists (for example a clone of a deleted quick deploy instance). An error in that step is logged and does not undo the instance delete.

**Cleanup policy conditions.** A condition is a comma-separated list of `key:value` pairs. All pairs must match.

| Condition | Meaning |
|---|---|
| `status:<status>` | The instance status is `<status>`. |
| `idle_days:N` | No deploy for N days (the creation time when the instance was never deployed). |
| `age_days:N` | The instance was created more than N days ago. This is not the time since the stop. |
| `stopped_days:N` | The instance is stopped, and the last stop finished N or more days ago. Instances without a recorded stop time never match. |
| `ttl_expired` | The expiry time is in the past. |

Use `stopped_days:N` for "stopped for N days".

- The stop time (`stopped_at`) is set when a stop finishes (API, TTL reaper, cleanup policy). A deploy or a clean clears it.
- The upgrade sets `stopped_at` for instances that are already stopped. It uses the last successful stop log, or else `updated_at`.
- Existing policies with `status:stopped,age_days:N` keep their meaning: created more than N days ago.

**Cleanup policy update.** `PUT /admin/cleanup-policies/:id` is a partial update. Only the fields in the body change, for example `{"enabled": false}` or `{"dry_run": true}`. The API validates the merged policy, and the scheduler reloads. The Enabled and Dry Run switches in the policy table use this call.

### Custom Actions

An operator can register custom actions on the server, for example "refresh-db" or "load seed data" (see [EXTENDING.md](EXTENDING.md)). A custom action runs on one stack instance.

- The instance detail page has an **Actions** menu. The custom actions are the first group of the menu. The menu also has Export values, Clone, and (for a user who can change the instance) Clean namespace and Delete.
- Every user who can see the instance sees the custom actions. Only the owner, an `admin`, or a `devops` user can run an action. For other users the items are disabled.
- A click on an action opens a dialog. The dialog shows the description of the action and, if the action has one, a warning text. Read the warning before you click **Run**.
- Some actions have parameters. The dialog shows a form with a text field, a switch, or a list. A field marked with `*` is required.
- After the run, the dialog shows the status code and the result of the action. A status code that is not 2xx is an error.
- Some actions start a job that runs in the background. The dialog then shows the job log and the job status (`running`, `succeeded`, `failed`).
  - The log updates every 3 seconds until the job ends.
  - When the server is busy or not reachable, the dialog shows "Waiting…" and tries again later.
  - Click **Stop following** to stop the updates.
  - The dialog stops the updates after 60 minutes and keeps the last 1 MiB of log text.
  - Closing the dialog or stopping the updates does not stop the job. The job continues on the server.
- The CLI and other API clients use the same endpoints: `GET /api/v1/stack-instances/:id/actions`, `POST /api/v1/stack-instances/:id/actions/:name`, and `GET /api/v1/stack-instances/:id/actions/:name/jobs/:job_id/log?offset=N`.

### Value Override

A value override is a per-chart configuration override on a stack instance. The application deep-merges it with the chart defaults when it exports the Helm values. It substitutes the template variables (`{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}`, `{{.Owner}}`) at export time.

### Audit Log

The application records every mutating API call (POST, PUT, DELETE) with the user, action, entity type, entity ID, and time.

- Only `devops` and `admin` users can read the audit log (`GET /audit-logs`). Only `admin` users can export it (`GET /audit-logs/export`). Other users get 403, and the UI does not show the Audit Log page to them.
- A plain create, update, or delete call gets the action `create`, `update`, or `delete` and the entity type of the resource. The entity type is singular, for example `cluster`, `cleanup_policy`, or `api_key`.
- Other operations get their own action, with the entity that they act on. For example, `POST /stack-instances/:id/deploy` gives `deploy | stack_instance | <instance id>`. The same applies to these operations:
  - `stop`, `clean`, `rollback`, `extend_ttl`, `clone`, and `invoke_action` (the action name is in the details).
  - Template `publish`, `unpublish`, `instantiate`, and `clone`.
  - Definition `import` and `upgrade`.
  - Notification channel `test`.
  - Cluster `test_connection` and `set_default`.
  - Cleanup policy `run`.
  - User `disable`, `enable`, `reset_password`, and `change_role` (the old and the new role are in the details).
  - Instance `follow` and `unfollow`.
- Deploy, stop, clean, and rollback store the ID of the deployment log in the details.
- Some actions come from the application itself and not from an API call: `expired` (the TTL reaper), `cleanup_policy_executed` (the cleanup scheduler), and `interrupted` (the recovery of an interrupted operation).
- A bulk operation writes one entry for each instance or template that succeeded, with `"bulk": true` in the details.
- Marking notifications as read writes no entry. Quick deploy writes its own entry (`quick_deploy | stack_instance`).
- Filter on the entity type `stack_instance` and an instance ID to see all operations on that instance.
- A change of a nested resource keeps the parent ID as the entity ID and adds the child ID to the details. For example, `PUT /clusters/:id/shared-values/:valueId` gives the entity ID `<cluster id>` and `{"value_id": "..."}`. A nested create adds `created_id`. A deleted orphaned namespace has the namespace name as the entity ID.
- The upgrade renames the plural entity types of old create, update, and delete entries to the singular names (`clusters` to `cluster`, `api_keys` to `api_key`, `quotas` to `quota`, and so on). Old operation entries keep their old values (for example `create | deploy`), because the old entry does not identify the operation reliably.

### Notifications

**In-app notifications.** The lifecycle events of an instance (deploy, stop, clean, rollback, delete, expiry warning, cleanup policy action) give an in-app notification to the owner of the instance and to each follower.

- A receiver gets the notification only when the event type is on in the own notification preferences (Profile page). A receiver without a preference for the event type gets it.
- The Profile page lists every event type that the user can get as owner or follower. For `admin` and `devops` users it also lists the system events `cleanup.policy.executed`, `quota.warning`, and `secret.expiring`. The system notifications to `admin` and `devops` users also follow these preferences.
- `PUT /notifications/preferences` accepts only these event types. An unknown event type gives 400, and the request changes no preference. Notification channels accept the same event types.
- Upgrade note: Before this version, the backend did not store a switched-off preference (it stored "on"), and it did not check the preferences. Users who switched events off before must open the Profile page, switch them off again, and save.
- An owner who also follows the instance gets one notification, not two.
- The user who started the operation is not excluded. For example, a follower who deploys the instance also gets the deploy notification. The owner always got the notifications of the own operations. Followers get the same rule.
- The WebSocket message `notification.new` goes only to the sockets of each receiver.

**Follow a stack instance.** The detail page of an instance has a **Follow** button next to the favorite star. It shows the number of followers.

- Every user who can see the instance can follow it (all signed-in users). The API is `POST /stack-instances/:id/follow` and `DELETE /stack-instances/:id/follow`. Both are idempotent and return `{"following": ..., "follower_count": ...}`. An unknown instance gives 404. The audit log records `follow` and `unfollow` on the instance.
- `GET /stack-instances/:id` adds `following` (the current user follows the instance) and `follower_count`. List responses do not have these fields.
- Followers get in-app notifications only. They do not get channel (webhook) deliveries.
- A favorite does not follow the instance.
- Deleting the instance deletes its followers. Deleting a user deletes the follows of the user.

**Notification channels** (`/admin/notification-channels`, `admin` and `devops`) send events as webhooks. Each channel has event-type subscriptions and optional **filters** that limit it to some instances:

| Filter | Value | Example |
|---|---|---|
| `instance_name_patterns` | Glob patterns for the instance name (`*`, `?`, `[a-z]`; `path.Match` syntax; case is ignored) | `test-*`, `*-eu` |
| `owner_ids` | User IDs of instance owners | The team members |
| `definition_ids` | Stack definition IDs | One product stack |
| `cluster_ids` | Cluster IDs | The development cluster |

- Each filter that you set must match (AND). Inside one filter, one value is enough (OR). A channel without filters gets the events of all instances.
- An event without an instance (quota warning, secret expiry) goes only to channels without filters.
- A cleanup policy run (`cleanup.policy.executed`) goes to a filtered channel when at least one affected instance matches.
- A channel that the filters skip gets no delivery log entry (the backend logs it at debug level).
- **Test** (`POST /admin/notification-channels/:id/test`) sends one test payload (event type `test`, one attempt, no redirect), also to a disabled channel. Each test send is in the delivery log with the event type `test`. The response has `success`, `message`, `status_code`, and `channel_enabled`.
- The backend does not follow a redirect from a channel webhook. A 3xx answer is a failed delivery: the delivery log has the status code and `HTTP <code>: redirect not followed`. Delivery log messages never contain the webhook URL. Configure the final URL.
- The save checks the patterns. An invalid pattern gives 400. An owner, definition, or cluster ID that does not exist gives a warning in the response (`warnings`), and the channel is saved. There are at most 50 values for each filter.
- The channel dialog has a **Filters** section: patterns as chips (press Enter after each pattern) and pickers for owners, definitions, and clusters. `admin` users pick owners from all users. `devops` users cannot read the user list (`GET /users` is for `admin` only). So their owner picker lists the users who own an instance now. The channel list shows "All instances" or a short summary of the filters.
- Migration 52 adds the column `notification_channels.filters` (JSON text, empty for existing channels). Migration 53 adds the table `instance_followers`.

### Cluster

A cluster is a registered Kubernetes cluster that instances can be deployed to.

- Each cluster stores connection details (a kubeconfig path or encrypted kubeconfig data). The application monitors it with periodic health checks.
- One cluster can be the **default** target.
- `admin` users manage clusters on the page `/admin/clusters`.
- A cluster with **in-cluster configuration** (`use_in_cluster`) uses the service account of the backend pod. It has no API server URL and no kubeconfig. The cluster dialog has a checkbox for it. The URL is then optional, and the list shows "in-cluster".
- Kubeconfig data needs `KUBECONFIG_ENCRYPTION_KEY`. Without the key, the backend rejects a cluster with kubeconfig data (400). Use a kubeconfig path or in-cluster configuration then.

**Cluster Health page** (`/admin/cluster-health`, `admin` and `devops`). The page shows CPU and memory as Kubernetes quantities in cores and binary units (`1620m` of `16` is 10 %, and `247 GiB`).

- The summary cards show the sum of the pod requests of the scheduled, unfinished pods (`requested_cpu` and `requested_memory` from `GET /clusters/:id/health/summary`) against the allocatable amount. The capacity is below.
- A pod counts like in the kube-scheduler: the larger value of its init phase and its app containers plus native sidecars, plus the pod overhead.
- Only this endpoint lists the pods (from the API server cache). The dashboard does not.
- Requests are not real use. The page does not read metrics-server.
- "Namespace Resource Usage" shows the quota requests (`requests.cpu` and `requests.memory` used) against the quota.

The Cluster Health widget on the dashboard shows only to `admin` and `devops` users. The API sends the health status only to these roles (the same rule as `GET /clusters`), so role `user` does not see the widget.

A cluster can store **container registry credentials** (`registry_url`, `registry_username`, `registry_password`, `image_pull_secret_name`) for automatic image pull secret provisioning.

- When you set them, the backend creates a `kubernetes.io/dockerconfigjson` secret in each stack namespace before the chart installs.
- A background service refreshes the secret every 4 hours, for short-lived tokens (for example registry tokens).
- The backend encrypts the registry password at rest with the same AES-GCM scheme as the kubeconfig data. This needs `KUBECONFIG_ENCRYPTION_KEY`. Without the key, the password is stored as plain text.

### Orphaned Namespaces

`/admin/orphaned-namespaces` (`admin`) lists `stack-*` namespaces without an instance. It shows pod, deployment, and service counts and Helm releases with `?details=true`.

- The deployer labels each namespace that it creates `managed-by=k8s-stack-manager` (and `k8s-stack-manager.io/instance-name`, see "Namespace labels"). The page lists labeled namespaces as orphans. It lists unlabeled `stack-*` namespaces in a separate "Unmanaged namespaces" table.
- `DELETE /admin/orphaned-namespaces/:namespace` deletes a labeled namespace. An unlabeled namespace can belong to another team or tool. The delete returns 409 unless `?confirm=<full namespace name>` is set. The page asks the `admin` to type the full name. A missing namespace gives 404.
- After a delete, the page removes the row. The namespace can stay in the phase `Terminating` for some time. A refresh then shows it with the delete button disabled.

### Analytics

`/admin/analytics` shows the overview and the template statistics to `admin` and `devops` users. The user activity (`GET /analytics/users`) is for `admin` only. The page does not request it for `devops`. Each section loads on its own, so one failed request does not hide the others. A template without deploys shows "—" as the success rate.

- The per-user deploy counts and the last activity come from the deploy logs. Each deploy log stores the user who started it (`user_id`). The data stays after the instance is deleted. A deploy on the instance of another user counts for the user who started it.
- Migration 49 adds an index on (`user_id`, `action`, `started_at`, `completed_at`, `status`). It also sets `user_id` on older deploy logs to the owner of the instance (in batches of 1000), when the instance still exists. Deploys of instances that were deleted before the upgrade stay without a user.
- "Instances" is the number of instances that the user owns now.

### Users

`/admin/users` (`admin`) shows the sign-in method (Local or SSO) and the status (Active or Disabled) of each user.

- Disable (with a confirmation) and Enable call `PUT /users/:id/disable` and `/enable`.
- An `admin` cannot disable, enable, or delete the own account. The buttons on the own row are disabled.
- Reset password shows only for local users.

Rules for delete, disable, enable, and role change (guarded admin changes):

- The handler reads the user first. It uses the stored ID for the self-check, the change, and the revocation. MySQL compares IDs without case, so `/users/ADMIN-ID` is the own account of `admin-id`.
- The change runs in one database transaction. The transaction locks the admin rows (ordered by ID), then the target row. Inside the lock, the caller must still be an enabled `admin`. Otherwise the API returns 403 "Admin role required" (for example when the caller was demoted or disabled after the token was issued).
- The last enabled `admin` cannot be demoted, disabled, or deleted (409). Two admins who demote, disable, or delete each other at the same time are serialized: exactly one change succeeds, and the other gets 403. An index on `users.role` (migration 50) keeps the lock on the admin rows.
- Every user change writes only its own columns (role, disabled, password hash, SSO profile). No change saves the full row. So a password reset or an SSO profile sync never undoes a concurrent role change or disable. An SSO login sets the role from the identity provider only for SSO users.

Edit role (local users only) opens a dialog with a role select (`user`, `devops`, `admin`) and a warning that the user is signed out. It calls `PUT /api/v1/users/:id/role` with `{"role": "devops"}`. The rules are:

- Only an `admin` can change a role.
- The role of an SSO user comes from the identity provider (`OIDC_ROLE_CLAIM`). It is set again at each SSO login. The API refuses the change with 409 "Role is managed by the identity provider". The page shows the role read-only with the hint "managed by SSO".
- An `admin` cannot change the own role (403). The button on the own row is disabled.
- The last enabled `admin` cannot lose the `admin` role (409), and the caller must still be an enabled `admin` (403). See the rules above.
- An unchanged role is a no-op: 200 with `"changed": false`. The sessions of the user stay valid.
- A change signs the user out (see [Revoking a User](#revoking-a-user)). The next request needs a new login, which gives a token with the new role. API keys stay valid and use the new role at once, because the API-key auth reads the role from the database on each request.
- The change writes an audit entry `change_role | user | <user id>` with `old_role` and `new_role` in the details.

## Architecture

### Data Flow

```
Template -> (instantiate) -> Definition + ChartConfigs -> (create instance) -> Instance + ValueOverrides
```

### Storage

- **MySQL** (GORM) is the only data store for all domain entities.
- The factory in `internal/database/repository.go` initializes the repository at startup.

### Authentication

- Authentication uses a JWT with the header `Authorization: Bearer <token>`, or an API key.
- The role hierarchy is `admin` > `devops` > `user`.
- `POST /auth/register` needs a valid token. With `SELF_REGISTRATION=false` (the backend default), only an `admin` can register users. With `SELF_REGISTRATION=true`, any signed-in user can register a new `user` account. Only an `admin` can set another role or create a service account. The Helm chart and `docker-compose.yml` set `SELF_REGISTRATION` to `true` by default.
- The backend creates the initial admin user at startup from `ADMIN_USERNAME` (default `admin`) and `ADMIN_PASSWORD`, when this user does not exist. A change of `ADMIN_PASSWORD` later does not change the stored password.

**OIDC single sign-on.** Set `OIDC_ENABLED=true` to turn on OpenID Connect (Authorization Code flow with PKCE). The backend then needs `OIDC_PROVIDER_URL`, `OIDC_CLIENT_ID`, and `OIDC_REDIRECT_URL`.

| Variable | Default | Effect |
|---|---|---|
| `OIDC_ENABLED` | `false` | Turn on OIDC sign-in. |
| `OIDC_PROVIDER_URL` | empty | Issuer URL of the identity provider. Required when OIDC is on. |
| `OIDC_CLIENT_ID` | empty | Client ID. Required when OIDC is on. |
| `OIDC_CLIENT_SECRET` | empty | Client secret. |
| `OIDC_REDIRECT_URL` | empty | Callback URL (`.../api/v1/auth/oidc/callback`). Required when OIDC is on. |
| `OIDC_SCOPES` | `openid,profile,email` | Scopes that the backend requests. |
| `OIDC_ROLE_CLAIM` | `roles` | Claim in the token that has the roles of the user. |
| `OIDC_ADMIN_ROLES`, `OIDC_DEVOPS_ROLES` | empty | Comma-separated role values. A match gives the role `admin` (checked first) or `devops`. All other users get `user`. |
| `OIDC_AUTO_PROVISION` | `true` | Create a user at the first SSO login. |
| `OIDC_LOCAL_AUTH` | `false` (Helm chart and Docker Compose: `true`) | Allow password login for all users. With `false`, only service accounts can use a password. The initial admin user is a service account. |
| `OIDC_STATE_TTL` | `5m` | Time that a started SSO login stays valid. |

### Stack Instance Permissions

| Operation | Owner | `admin` | `devops` | Other `user` |
|---|---|---|---|---|
| View the instance, status, pods, deploy log, access URLs; export values; compare; clone; list actions | yes | yes | yes | yes |
| Deploy, deploy preview, stop, clean, delete, rollback, run actions, read action job logs | yes | yes | yes | no (403) |
| Edit the instance, extend the TTL | yes | yes | yes | no (403) |
| Read or change value, branch, and quota overrides | yes | yes | yes | no (403) |
| Set a quota override above the cluster quota | no (403) | yes | yes | no (403) |

- Bulk operations (`/stack-instances/bulk/*`) need the `devops` or `admin` role.
- A refused request returns 403 before any side effect: no hook, no status change, no deploy-log entry, and no Helm call. The backend logs the refused attempt.
- The web UI hides the lifecycle buttons and shows the page read-only for a user who cannot change the instance.
- A clone always belongs to the user who creates it.
- **Quota override limit.** An owner without the `admin` or `devops` role can set a quota override only at or below the cluster quota of the instance's cluster.
  - The check covers `cpu_request`, `cpu_limit`, `memory_request`, `memory_limit`, `storage_limit`, `ephemeral_storage_limit`, the `container_*` defaults and maximum values, and `pod_limit`.
  - The API compares each value with the same cluster value as a Kubernetes quantity, so `16000m` equals `16`.
  - A value above it gets 403, for example `cpu_limit 64 exceeds the cluster quota 16; only admin or devops can set a higher quota`.
  - A value equal to the stored override passes. So an owner can change one field without losing an admin grant on another field. Raising a value, or a new value above the cluster quota, still gets 403.
  - A field without a cluster value has no limit.
  - `pod_limit: 0` means no pod limit, so it counts as above a cluster pod limit.
  - `admin` and `devops` users can grant more.
  - The check runs when the override is saved. A deploy does not check again.
- `PUT /stack-instances/:id/quota-overrides` replaces the whole override. A field that you do not send is cleared, and the cluster quota applies to it. To change one field, read the override with `GET`, change the field, and send all fields.
- `GET /stack-instances/:id/branches/:chartId` returns the branch override of one chart (404 when there is none). The same rule as for the list applies.
- The override restriction limits who can *edit* through the override endpoints. It is not a secrecy control. The merged values (export, compare, deploy-log values) and a clone still contain the override values. Do not put secrets in value overrides. Use Kubernetes Secrets or an external secret store.
- The same applies to cluster **shared values**. Only `admin` users can edit them, but every user sees them in the exported and compared values of any instance on that cluster. Do not put secrets in shared values.

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

- The idle time counts from the last authenticated request, not from the last refresh. Each request with a session access token updates the session activity (at most one database write each minute for each session). API-key requests do not change sessions.
- A refresh never extends the session. A new refresh token expires at the earlier time of `now + REFRESH_TOKEN_EXPIRATION` and `login time + SESSION_MAX_LIFETIME`.
- After `SESSION_MAX_LIFETIME`, the user must log in again. An SSO login reads the role and the account state from the identity provider again. So a role change in the identity provider applies at the latest after `SESSION_MAX_LIFETIME` plus one `ACCESS_TOKEN_EXPIRATION`.
- Two browser tabs can refresh at the same time with the same cookie. The second request presents a token that the first request just rotated. Inside `REFRESH_REUSE_GRACE`, it gets a new access token and no new cookie. Nothing is revoked.
- Any other reuse of a used refresh token revokes the session family (replay protection). Other sessions of the user stay active. A token that logout revoked never gets the grace.
- An access token stays valid until it expires, also when the session idles out. Revocation (see below) uses the token blocklist instead.

Note: An `admin` can change the role of a local user (`PUT /api/v1/users/:id/role`, see Users). The change revokes the sessions of the user, so it applies to the next request. For SSO users, the identity provider controls the role. The role syncs at each SSO login.

Exceptions to the session limits:

- **CLI token from the SSO CLI login** (`stackctl login --sso`). This is a long-lived access token (`JWT_EXPIRATION`, default 24h) without a session. `SESSION_IDLE_TIMEOUT` and `SESSION_MAX_LIFETIME` do not apply. It keeps the role that it had at the login until it expires. Revoking the user (table below) still rejects it.
- **API keys** have no session. They use the current role of the user in the database. They stay valid until they expire or you revoke them.
- **WebSocket** traffic (`/ws`) does not count as activity for the idle limit. A tab that only receives live updates idles out after `SESSION_IDLE_TIMEOUT`. The server closes a socket when its access token expires. The web UI reconnects with the current token.
- **Lost refresh response.** If the server rotates the refresh token but the browser never gets the response (network drop, client timeout), the browser keeps the used cookie. A retry within `REFRESH_REUSE_GRACE` still works, but sets no new cookie. The next refresh after the grace window counts as a replay and ends the session. The user must log in again. This fails safe by design.

### Revoking a User

| Action | Access tokens issued before | Refresh tokens | API keys | Open WebSocket connections |
|---|---|---|---|---|
| Delete the user | rejected (401) | revoked | deleted | closed |
| Disable the user | rejected (401) | revoked | kept, but rejected while the user is disabled | closed |
| Reset the password | rejected (401) | revoked | kept | closed |
| Change the role | rejected (401) | revoked | kept (they use the new role at once) | closed |
| Log out (`/auth/logout`) | the current token is rejected (401) | the presented token is revoked | kept | the sockets of the current token are closed |
| Log out of all sessions (`/auth/logout-all`) | the current token is rejected (401) | all revoked | kept | all sockets of the user are closed |
| Delete an API key (`DELETE /users/:id/api-keys/:keyId`) | not affected | not affected | the key is deleted | the sockets opened with the key are closed |

A new login after a password reset or a role change works at once, also in the same second.

- The login cache (`LOGIN_CACHE_TTL`) only skips the password check. After the password check, the login reads the user again. It uses that record for the disabled check and the token role. A password that changes during the login gives 401.
- Only tokens issued before the action are rejected. Access tokens have an `iat_ms` claim (issue time in milliseconds), and the user block stores its time in milliseconds. So the check has millisecond precision.
- A token without `iat_ms` (issued by an older version) is rejected when it was issued in the second of the block or before it.
- The block time and the token issue time come from the clocks of different replicas. So the replicas need synchronized clocks (NTP, Network Time Protocol).
- During a rolling update with replicas of different versions, two blocks of the same user in the same second use the millisecond time of the block from the newer version.
- Enabling a disabled user does not bring back the tokens that were issued before the disable.

WebSocket connections (`/ws`):

- The upgrade runs the same checks as the HTTP API. A revoked token, a token issued at or before a user block, a deleted user, and a disabled user get 401. If the session store fails, the check passes and the backend logs the error (the same policy as the HTTP API).
- An API key works too (for example `stackctl stack deploy -f` and `stackctl stack watch` in CI). Send it in the `X-API-Key` header. The server never reads an API key from the query string.
  - The upgrade runs the checks of the HTTP API-key auth. An unknown, deleted, or expired key, a deleted user, and a disabled user get 401. The role comes from the database.
  - A JWT in the subprotocol or the `Authorization` header wins over the key.
  - An API-key upgrade counts against the per-IP API rate limit (`RATE_LIMIT`) before the key lookup. Over the limit, it gets 429.
  - At most 4 Argon2id key verifications run at the same time in a backend process (HTTP and `/ws`). A request that finds no free slot within 100 ms gets 503 `Server busy, retry later`.
  - A socket that was opened with a key closes (close code 1008, reason `session revoked`) in these cases: the key is deleted, or the user is deleted, disabled, reset, or changes role (table above).
  - Note: A password reset or a role change closes the API-key sockets of the user. HTTP requests with the key keep working, and a new socket with the key works at once. The key itself is not revoked.
  - The socket also closes when the key expires (reason `api key expired`). `stackctl stack watch` and `stackctl stack deploy -f` then stop with the message `Connection closed by the server: <reason>`. Run the command again.
- The server closes an open socket (close code 1008) when the user is revoked or the session logs out (table above), and when the access token of the socket expires. The web UI then reconnects. It refreshes the token first when the server closed the socket with 1008 or the token expires within 30 seconds. It stops when the refresh is rejected. The refresh does not count as activity for `SESSION_IDLE_TIMEOUT`.
- Every minute, each backend replica checks its open sockets again (token blocklist, user blocklist, deleted or expired API key, deleted or disabled user) and closes the revoked ones. The user blocklist applies to API-key sockets with the connection time, so a user revocation also closes them on replicas without fan-out. The API keys of one check are read in one query. If the session store or the database fails, the check passes and the backend logs the error.
- After `/auth/logout-all`, the other sessions of the user keep a valid access token until it expires. Their sockets can reconnect until then.
- With more than one backend replica and WebSocket fan-out on (`WS_FANOUT_ENABLED=true`, the Helm default), a revoke closes the sockets on all replicas within about half a second. The replica that handles the revoke request closes its own sockets at once. Without fan-out, the other replicas close them at the next check, within about one minute, or at token expiry if that is earlier. The check stays on as a fallback in both cases. It also covers long-lived tokens (`JWT_EXPIRATION`, for example the CLI token and the mode without refresh tokens).

### Multi-Cluster

- You register clusters through the API with a kubeconfig path or kubeconfig data (encrypted at rest with AES-GCM), or with the in-cluster configuration.
- `ClusterRegistry` manages the Kubernetes and Helm clients of each cluster.
- A health poller periodically checks the connection of each cluster and updates the status.
- Instances target a specific cluster (or the default cluster).
- The `deployer` package routes deploy, undeploy, and status operations through the registry to the correct cluster.
- Per-cluster container registry credentials enable automatic image pull secret provisioning. A `SecretRefresher` runs every 4 hours to keep tokens current.

### Backend Replicas

- Every backend replica serves the API and the WebSocket clients.
- Only one replica, the leader, runs the background jobs: TTL reaper, expiry warning, cleanup policies, quota and secret warnings, pull secret refresh, cluster health checks, the k8s status watcher, and the stack-access reconcile. So each job runs once, also with 2 or more replicas.
- The replicas elect the leader with a Kubernetes Lease (`LEADER_ELECTION_ENABLED=true`, on in the Helm chart). When the leader stops or loses the lease, another replica takes over after a few seconds (about 2 seconds on a normal shutdown, at most about 15 seconds after a crash).
- Without election (`LEADER_ELECTION_ENABLED=false`, the default for Docker Compose and local development), every process runs the jobs. Use one replica then.
- A cleanup policy change takes effect on the leader within one minute. A scheduled policy run that fell into a leader change runs when the new leader starts (when it was due in the last 5 minutes).
- After a leader change, one quota or secret warning can come again (the cooldown of these warnings is in memory).
- Every replica writes a heartbeat to the database every 30 seconds. The leader ends the deploys, rollbacks, stops, and cleans of a replica without a heartbeat for 2 minutes (see Interrupted operation above).
- The metric `stackmanager_leader` is 1 on the leader. See [ARCHITECTURE.md](ARCHITECTURE.md#replica-model).
- The replicas share live updates (deploy logs, status changes, cluster health, notifications, socket revocations) through the database table `ws_events` (`WS_FANOUT_ENABLED=true`, on in the Helm chart). A client on any replica sees the events of all replicas. Events from another replica arrive up to about half a second later (`WS_FANOUT_POLL_INTERVAL`, 500ms).
- Without fan-out (`WS_FANOUT_ENABLED=false`, the default for Docker Compose and local development), a client sees only the events of its own replica. Use one replica then.
- A message larger than 512 KiB, or a message sent while the fan-out buffer is full, reaches only the clients of the replica that sent it. After a database outage, events older than 30 seconds are not delivered. The page shows the current state after a reload.
- An in-app notification goes only to the sockets of the notified user.

### Git Integration

- The application detects the provider from the repository URL. `dev.azure.com` or `visualstudio.com` gives Azure DevOps. `gitlab.com` or the host of `GITLAB_BASE_URL` gives GitLab. Any other URL is not supported.
- Branch listing uses an in-memory cache (5-minute TTL).
- The application uses service-level tokens (PAT or token), not tokens of single users.

### Helm Values

- The deep merge order, lowest first: cluster shared values (by priority, then name in byte order, then ID), chart defaults, instance overrides, template locked values. A locked value always wins.
- Template variable substitution: `{{.Branch}}`, `{{.Namespace}}`, `{{.InstanceName}}`, `{{.StackName}}`, `{{.Owner}}`.
- The same merge feeds deploy, deploy preview, bulk deploy, quick deploy, export, and compare.
- Export: `GET /stack-instances/:id/values` (ZIP, one `values.yaml` for each chart) or `GET /stack-instances/:id/values/:chartId` (YAML).

### Branding

The frontend image is the same for every installation. The product name, the logo, and the favicon come from the backend at runtime.

| Variable | Default | Effect |
|---|---|---|
| `APP_TITLE` | `K8s Stack Manager` | Name in the browser tab, sidebar, app bar, login page, and setup wizard. Maximum 100 characters. |
| `APP_LOGO_URL` | empty | Logo image. Empty means the built-in logo (`/logo.svg`). |
| `APP_FAVICON_URL` | `/favicon.svg` | Browser tab icon (built-in). |

- `GET /api/v1/ui-config` returns `{"title", "logo_url", "favicon_url"}`. It needs no token (the login page uses it). It has the API rate limit and `Cache-Control: public, max-age=300`.
- The web UI renders at once with the last config that it received (browser localStorage key `ui-config`) or, on the first visit, the built-in values. It loads the config once at startup, swaps in the values when they arrive (also on the login page), and stores them for the next start. If the call fails or takes more than 5 seconds, the initial values stay. A logo that does not load falls back to the built-in logo.
- URL rule: a same-origin path (`/branding/logo.svg`) or an `https://` URL with a host. The backend refuses other values at startup with a clear error. It refuses these values: `http://` (mixed content, clear text), `javascript:` and other schemes, and all `data:` URLs (an SVG data URL can carry script). It also refuses protocol-relative URLs (`//host/x`), paths without a leading `/`, user info, spaces, and control characters. A path resolves against the frontend origin, not the API.
- Helm: `branding.title`, `branding.logoUrl`, and `branding.faviconUrl` set the variables (an explicit `backend.env` value wins). `branding.files` (text, for example SVG) and `branding.binaryFiles` (base64, for example PNG or ICO) put the files in a ConfigMap. The ConfigMap has file names that match `^[-._a-zA-Z0-9]+$` and at most 900 KiB in total (otherwise the render fails). The frontend nginx serves the files at `/branding/<name>`, with the content type of the extension, `X-Content-Type-Options: nosniff`, and a sandbox Content Security Policy. Example:

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
- Go 1.26 or later (backend)
- Node.js 22.12 or later (frontend). CI and the Docker images use Node.js 26.

### Running

```bash
make dev              # Full stack with Docker Compose
make dev-local        # MySQL (Docker), and backend and frontend locally with hot reload
```

### Testing

```bash
make test             # All unit tests
make test-backend-all # Backend unit and integration tests (starts MySQL)
make test-e2e         # End-to-end tests with Playwright
```

### Adding a New API Resource

See `.github/instructions/api-extension.instructions.md` for the step-by-step guide.

## Troubleshooting

- **JWT errors.** Make sure that `JWT_SECRET` is set and has at least 16 characters.
- **Database connection errors (MySQL).** Make sure that MySQL runs. Check the environment variables `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, and `DB_NAME`.
- **Git provider errors.** Check that `AZURE_DEVOPS_PAT` (or `AZURE_DEVOPS_AUTH=workload-identity` with the `AZURE_*` variables) or `GITLAB_TOKEN` is correct. An empty token is valid. The provider is then not available.
- **Cluster connection errors.** Check that the kubeconfig path or data is valid. Use the "Test Connection" button on the Clusters admin page. If `KUBECONFIG_ENCRYPTION_KEY` is set, the backend encrypts all kubeconfig data at rest. A different key makes existing encrypted data unreadable.
- **A chart is rejected with "chart source not allowed".** The chart is not in a repository of `ALLOWED_CHART_REPOS`. Add the repository to the list, or change the chart config.
- **The UI cannot reach the API behind a port-forward.** The frontend pod does not proxy the API. Open the UI through the ingress.
