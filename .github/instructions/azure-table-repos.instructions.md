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
- Always handle `azcore.ResponseError` and map to domain errors from `pkg/dberrors`
- Entity JSON field names must be PascalCase for Azure Tables compatibility
- Include `Timestamp` field for optimistic concurrency on updates
- Use `context.Context` on all repository methods for cancellation and timeout support
- Return `dberrors.ErrNotFound` when entity doesn't exist, not raw Azure errors
