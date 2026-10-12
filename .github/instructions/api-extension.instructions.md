---
applyTo: "**"
---

# API Extension Instructions

## Two kinds of resource

- **Item-shaped resource**: uses the generic `models.Repository`, the generic `Handler`, a numeric ID, `Base`, and `Version`. Only `Item` does this today. The steps below use `Order` as an Item-shaped example.
- **Domain resource** (all real resources): a dedicated model, repository interface, repository, and handler struct. The ID is a string UUID (`gorm:"primaryKey;size:36"`) that the repository sets with `uuid.New().String()`. The model does not embed `Base` and has no `Version` column. Copy a recent resource (`instance_follower.go`, `notification_channel.go`).

## Adding a new resource (step by step)

### 1. Define the model

Create `internal/models/<entity>.go`. Put the repository interface in the same file. `models.go` holds only the shared types (`Base`, `Item`, `Validator`, `Versionable`, `Repository`, `GenericRepository`, `Filter`, `Pagination`).

Item-shaped example:

```go
type Order struct {
    Base
    UserID  uint    `gorm:"not null" json:"user_id"`
    Total   float64 `gorm:"not null" json:"total"`
    Status  string  `gorm:"size:50;not null;default:'pending'" json:"status"`
    Version uint    `gorm:"not null;default:1" json:"version"` // optimistic locking (1 = initial; 0 = not sent)
}
```

For a domain model, replace `Base` with `ID string`, `CreatedAt`, and `UpdatedAt`. Put 8-byte fields first.

### 2. Add validation

Add `Validate()` to `internal/models/validation.go`. It implements `Validator`. The generic repository calls it on create and update. Call it from the handler or the repository for a domain model.

```go
func (o *Order) Validate() error {
    if o.UserID == 0 {
        return errors.New("user_id is required")
    }
    return nil
}
```

### 3. Add a migration

Add a named function that returns `schema.Migration` in `internal/database/migrations.go`. Register it with `migrator.AddMigration(...)`. The version is a date plus a 6-digit sequence. The last one is `20261012000058`. Migrations run at startup.

```go
func createOrdersMigration() schema.Migration {
    return schema.Migration{
        Version:     "<next version>",
        Name:        "create_orders_table",
        Description: "Create orders table",
        Up:          func(tx *gorm.DB) error { return tx.AutoMigrate(&models.Order{}) },
        Down: func(tx *gorm.DB) error {
            if tx.Migrator().HasTable(&models.Order{}) {
                return tx.Migrator().DropTable(&models.Order{})
            }
            return nil
        },
    }
}
```

Make `Up` and `Down` idempotent. Add indexes in a migration (see `scalability.instructions.md`).

### 4. Add the repository (domain resource)

Create `internal/database/<entity>_repository.go`. Wire it in `internal/database/repository_factory.go`. Newer methods take `context.Context` first. Use `BaseRepository[T]` for plain find, create, and delete. Return `dberrors.NewDatabaseError(op, err)`.

### 5. Create the handler

Create `internal/api/handlers/orders.go`. A domain resource has a dedicated handler struct with its repositories (for example `OrderHandler`). The generic `Handler` is only for `Item`.

```go
func (h *OrderHandler) CreateOrder(c *gin.Context) {
    var order models.Order
    if err := c.ShouldBindJSON(&order); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
        return
    }
    if err := h.orders.Create(&order); err != nil {
        status, message := mapError(err, "Order") // items.go uses handleDBError
        c.JSON(status, gin.H{"error": message})
        return
    }
    c.JSON(http.StatusCreated, order)
}
```

Follow `handlers/items.go` for the create, read, update, delete (CRUD) shape and `stack_definitions.go` for a domain handler.

### 6. Register the routes

Add the handler to `Deps` (`internal/api/routes/routes.go`). Build it in `buildHandlers` and pass it in `buildRouter` (`api/bootstrap.go`). Register the routes under the `authed` group inside `if deps.XHandler != nil`. That group applies `CombinedAuth`, `SpanEnrichUser`, and the audit middleware. Add `RequireDevOps()` or `RequireAdmin()` where the role rules need it.

```go
if deps.OrderHandler != nil {
    orders := authed.Group("/orders")
    {
        orders.GET("", deps.OrderHandler.ListOrders)
        orders.POST("", devops, deps.OrderHandler.CreateOrder)
    }
}
```

The `Item` routes show the generic form: `handlers.NewHandlerWithHub(deps.Repository, deps.Hub)`.

### 7. Check the audit entry

Each successful POST, PUT, and DELETE creates an audit entry (`middleware.NewAuditMiddleware`). A route that is not plain CRUD needs an entry in `auditRoutes` (`middleware/audit.go`). Add new actions and entity types to `KnownAuditActions` and `KnownAuditEntityTypes`, and to the filter lists of `frontend/src/pages/AuditLog`. `routes/audit_routes_test.go` fails for an unknown entity type.

### 8. Add Swagger annotations

```go
// CreateOrder godoc
// @Summary Create a new order
// @Tags orders
// @Accept json
// @Produce json
// @Param order body models.Order true "Order object"
// @Success 201 {object} models.Order
// @Failure 400 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Failure 500 {object} map[string]string
// @Router /api/v1/orders [post]
```

Regenerate the docs: `cd backend && make docs`. Commit the changed `backend/docs/swagger.json`, `swagger.yaml`, and `docs.go`.

### 9. Write tests

Create `internal/api/handlers/orders_test.go`.

- `Item`: use `setupTestRouter()` and `MockRepository` (see `items_test.go`).
- Domain resource: add a mock to `mock_domain_repositories_test.go` and write a router helper with `callerID` and `callerRole`. See `definition_access_test.go`.
- Use table-driven tests with `t.Parallel()` on the parent and each subtest. Write `tt := tt`.
- Validate responses with `gojsonschema` (`test_schemas.go`).

### 10. Add the frontend

Add service methods in `frontend/src/api/client.ts` with TSDoc on the service object and on each method:

```typescript
/** Order service. Maps to `/api/v1/orders`. */
export const orderService = {
  /**
   * Fetch all orders.
   * @returns Array of orders
   * @see GET /api/v1/orders
   */
  list: async (): Promise<Order[]> => (await api.get('/api/v1/orders')).data,
  /**
   * Fetch one order.
   * @param id - The order ID
   * @returns The order
   * @see GET /api/v1/orders/:id
   */
  get: async (id: string): Promise<Order> => (await api.get(`/api/v1/orders/${id}`)).data,
};
```

Create the page in `frontend/src/pages/Orders/index.tsx`. Register it in `routes.tsx`. Add the nav item in `components/Layout/index.tsx`.

## Key patterns

- **Errors**: `mapError(err, "Entity")` (`handlers/errors.go`) in domain handlers. `handleDBError()` only in the `Item` handlers. Both return safe messages. A 500 returns `"Internal server error"`.
- **IDs**: `Item` IDs are numeric (`parseUintParam`, 400 on error). Domain IDs are strings. A missing entity gives 404 through `mapError`.
- **Responses**: success returns the entity. An error returns `gin.H{"error": "message"}`.
- **Lists**: a new or changed list endpoint takes `page` and `pageSize` and calls `ListPaged(..., limit, offset)` with a filter struct and column projection. Small admin lists stay unpaged (`List()`). `Item` lists use `models.Filter` and `models.Pagination`.
- **Roles**: `RequireAdmin()`, `RequireDevOps()`, `requireInstanceModify`. Definition changes need the owner, `devops`, or `admin`. Create definition and chart add or remove need `devops` or `admin`.
- **Middleware**: Cross-Origin Resource Sharing (CORS), Logger, and Recovery are global. The API rate limiter applies to the `/api/v1` group. Login has a stricter limiter.
