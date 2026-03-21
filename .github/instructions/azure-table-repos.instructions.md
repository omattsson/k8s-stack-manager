---
description: "Use when creating or modifying Azure Table Storage repository implementations"
applyTo: "backend/internal/database/azure/**/*.go"
---

# Azure Table Repository Guidelines

- Follow the existing `table.go` CRUD pattern for all new repositories
- Use PartitionKey + RowKey design documented in PLAN.md:
  - Users: PK="users", RK=username
  - StackDefinitions: PK="global", RK=definition_id
  - ChartConfigs: PK=stack_definition_id, RK=chart_config_id
  - StackInstances: PK="global", RK=instance_id
  - ValueOverrides: PK=stack_instance_id, RK=chart_config_id
  - AuditLogs: PK=YYYY-MM, RK=reverse_timestamp+uuid
  - StackTemplates: PK="global", RK=template_id
  - TemplateChartConfigs: PK=stack_template_id, RK=chart_config_id
  - APIKeys: PK=user_id, RK=key_id
  - DeploymentLogs: PK=instance_id, RK=reverse_timestamp+uuid
  - Clusters: PK="clusters", RK=cluster_id
- Always handle `azcore.ResponseError` and map to domain errors from `pkg/dberrors`
- Entity JSON field names must be PascalCase for Azure Tables compatibility
- Include `Timestamp` field for optimistic concurrency on updates
- Generic `models.Repository` accepts `context.Context`; domain-specific repository interfaces do not accept context — implementations use `context.Background()` internally
- Return `dberrors.ErrNotFound` when entity doesn't exist, not raw Azure errors
