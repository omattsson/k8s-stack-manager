# Azure Table Repository Guidelines

- Follow the existing `table.go` CRUD pattern for all new repositories
- Use PartitionKey + RowKey design documented in PLAN.md:
  - Users: PK="users", RK=username
  - StackDefinitions: PK="global", RK=definition_id
  - ChartConfigs: PK=stack_definition_id, RK=chart_config_id
  - StackInstances: PK="global", RK=instance_id
  - ValueOverrides: PK=stack_instance_id, RK=chart_config_id
  - StackTemplates: PK="global", RK=template_id
  - TemplateChartConfigs: PK=stack_template_id, RK=chart_config_id
  - APIKeys: PK=user_id, RK=key_id
  - AuditLogs: PK=YYYY-MM, RK=reverse_timestamp+uuid
- Always handle `azcore.ResponseError` and map to domain errors from `pkg/dberrors`
- Entity JSON field names must be PascalCase for Azure Tables compatibility
- Optimistic concurrency uses two layers: application-level `Version` field check + Azure ETag/IfMatch on `UpdateEntity` (HTTP 412 on conflict)
- `models.Repository` (generic) accepts `context.Context`; domain-specific repositories currently use `context.Background()` internally
- Return `dberrors.ErrNotFound` when entity doesn't exist, not raw Azure errors
