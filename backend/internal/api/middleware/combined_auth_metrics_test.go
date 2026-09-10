package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// stubAPIKeyRepo and stubUserRepo embed the interface so they satisfy it while
// overriding only the methods CombinedAuth calls.
type stubAPIKeyRepo struct {
	models.APIKeyRepository
	records []*models.APIKey
	err     error
}

func (s *stubAPIKeyRepo) FindByPrefix(string) ([]*models.APIKey, error)  { return s.records, s.err }
func (s *stubAPIKeyRepo) UpdateLastUsed(string, string, time.Time) error { return nil }

type stubUserRepo struct {
	models.UserRepository
	user *models.User
	err  error
}

func (s *stubUserRepo) FindByID(string) (*models.User, error) { return s.user, s.err }

func apiKeyStatusCount(t *testing.T, reader sdkmetric.Reader, status string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "auth.apikey.total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, dp := range sum.DataPoints {
				s, _ := dp.Attributes.Value(attribute.Key("status"))
				if s.AsString() == status {
					total += dp.Value
				}
			}
		}
	}
	return total
}

func TestCombinedAuth_APIKeyMetricOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	prevMeter := authMeter
	authMeter = mp.Meter("auth")
	initAuthMetrics()
	t.Cleanup(func() {
		authMeter = prevMeter
		otel.SetMeterProvider(prev)
		_ = mp.Shutdown(context.Background())
	})

	rawKey, prefix, hash, err := models.GenerateAPIKey()
	require.NoError(t, err)
	validKey := &models.APIKey{ID: "k1", UserID: "u1", Prefix: prefix, KeyHash: hash}

	do := func(deps APIKeyAuthDeps, header string) {
		r := gin.New()
		r.Use(CombinedAuth(deps))
		r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if header != "" {
			req.Header.Set("X-API-Key", header)
		}
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	// missing header
	do(APIKeyAuthDeps{APIKeyRepo: &stubAPIKeyRepo{}, UserRepo: &stubUserRepo{}}, "")
	// invalid: too-short key (raw < 16 chars)
	do(APIKeyAuthDeps{APIKeyRepo: &stubAPIKeyRepo{}, UserRepo: &stubUserRepo{}}, "sk_short0123456")
	// invalid: unknown prefix (not-found from repo)
	do(APIKeyAuthDeps{APIKeyRepo: &stubAPIKeyRepo{err: dberrors.NewDatabaseError("find_by_prefix", dberrors.ErrNotFound)}, UserRepo: &stubUserRepo{}}, "sk_"+rawKey)
	// failure: operational repo error
	do(APIKeyAuthDeps{APIKeyRepo: &stubAPIKeyRepo{err: errors.New("db down")}, UserRepo: &stubUserRepo{}}, "sk_"+rawKey)
	// disabled: valid key, disabled user
	do(APIKeyAuthDeps{
		APIKeyRepo: &stubAPIKeyRepo{records: []*models.APIKey{validKey}},
		UserRepo:   &stubUserRepo{user: &models.User{ID: "u1", Username: "svc", Disabled: true}},
	}, "sk_"+rawKey)
	// success: valid key, enabled user
	do(APIKeyAuthDeps{
		APIKeyRepo: &stubAPIKeyRepo{records: []*models.APIKey{validKey}},
		UserRepo:   &stubUserRepo{user: &models.User{ID: "u1", Username: "svc", Role: "devops"}},
	}, "sk_"+rawKey)

	assert.Equal(t, int64(1), apiKeyStatusCount(t, reader, "missing"))
	assert.Equal(t, int64(2), apiKeyStatusCount(t, reader, "invalid"), "short key + unknown prefix")
	assert.Equal(t, int64(1), apiKeyStatusCount(t, reader, "failure"))
	assert.Equal(t, int64(1), apiKeyStatusCount(t, reader, "disabled"))
	assert.Equal(t, int64(1), apiKeyStatusCount(t, reader, "success"))
}
