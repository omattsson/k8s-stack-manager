---
applyTo: "**"
---

# API Extension Instructions

## Adding a New Resource (Step-by-Step)

### 1. Define the Model
Add to `internal/models/models.go`. Embed `Base` for ID, timestamps, and soft-delete:
```go
type Order struct {
    Base
    UserID    uint    `gorm:"not null" json:"user_id"`
    Total     float64 `gorm:"not null" json:"total"`
    Status    string  `gorm:"size:50;not null;default:'pending'" json:"status"`
    Version   uint    `gorm:"not null;default:1" json:"version"` // optimistic locking (1 = initial; 0 = not provided)
}
```

### 2. Add Validation
Add a `Validate()` method in `internal/models/validation.go` implementing the `Validator` interface. The repository calls this automatically on Create/Update:
```go
func (o *Order) Validate() error {
    if o.UserID == 0 {
        return errors.New("user_id is required")
    }
    if o.Total < 0 {
        return errors.New("total must be non-negative")
    }
    return nil
}
```

### 3. Add a Migration
Append a new versioned migration in `internal/database/migrations.go`. Use incrementing version strings. Migrations run automatically on startup:
```go
migrator.AddMigration(schema.Migration{
    Version:     "20231201000003",
    Name:        "create_orders_table",
    Description: "Create orders table",
    Up: func(tx *gorm.DB) error {
        return tx.AutoMigrate(&models.Order{})
    },
    Down: func(tx *gorm.DB) error {
        return tx.Migrator().DropTable(&models.Order{})
    },
})
```

### 4. Create Handler File
Create `internal/api/handlers/orders.go`. Use the existing `Handler` struct — it already has the `Repository` and optional `BroadcastSender` dependencies:
```go
func (h *Handler) CreateOrder(c *gin.Context) {
    var order models.Order
    if err := c.ShouldBindJSON(&order); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
        return
    }
    if err := h.repository.Create(c.Request.Context(), &order); err != nil {
        status, message := handleDBError(err)
        c.JSON(status, gin.H{"error": message})
        return
    }
    c.JSON(http.StatusCreated, order)
}
```
Follow the full CRUD pattern from `handlers/items.go`: CreateX, GetX, GetXs (list), UpdateX, DeleteX.

### 5. Register Routes
Add to `internal/api/routes/routes.go` under the `/api/v1` group. Reuse the same `Handler` instance:
```go
orders := v1.Group("/orders")
{
    orders.GET("", itemsHandler.GetOrders)
    orders.GET("/:id", itemsHandler.GetOrder)
    orders.POST("", itemsHandler.CreateOrder)
    orders.PUT("/:id", itemsHandler.UpdateOrder)
    orders.DELETE("/:id", itemsHandler.DeleteOrder)
}
```
If the new resource needs its own dependencies beyond `Repository`, create a separate handler struct.

### 6. Add Swagger Annotations
Add godoc comments above each handler method:
```go
// CreateOrder godoc
// @Summary Create a new order
// @Tags orders
// @Accept json
// @Produce json
// @Param order body models.Order true "Order object"
// @Success 201 {object} models.Order
// @Failure 400 {object} map[string]string
// @Router /api/v1/orders [post]
```
Regenerate docs: `cd backend && make docs`

### 7. Write Tests
Create `internal/api/handlers/orders_test.go` following the pattern in `items_test.go`:
- Use `MockRepository` from `mock_repository.go` — extend it if needed for new entity types
- Use table-driven tests with `t.Parallel()` on parent and subtests
- Define JSON schemas in `test_schemas.go` for response validation
- Use `setupTestRouter()` pattern with `gin.TestMode` and `httptest.NewRecorder()`

### 8. Frontend Integration
Add API service methods in `frontend/src/api/client.ts`:
```typescript
export const orderService = {
  list: async () => (await api.get('/api/v1/orders')).data,
  get: async (id: number) => (await api.get(`/api/v1/orders/${id}`)).data,
  create: async (order: Order) => (await api.post('/api/v1/orders', order)).data,
};
```
Create a new page in `frontend/src/pages/Orders/index.tsx` and register in `routes.tsx`.

## Key Patterns to Follow
- **Error handling**: Use `handleDBError()` for all repository errors — it maps DB errors to correct HTTP status codes
- **ID parsing**: Always validate path params with `strconv.ParseUint` and return 400 for invalid IDs
- **Response format**: Success returns the entity directly; errors return `gin.H{"error": "message"}`
- **Filtering**: Use `models.Filter` and `models.Pagination` structs passed as conditions to `repository.List()`
- **Middleware**: Apply rate limiting to route groups that need it; CORS/Logger/Recovery are global
