package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/api/middleware"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func loginStatusCount(t *testing.T, reader sdkmetric.Reader, method, status string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "auth.login.total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			for _, dp := range sum.DataPoints {
				me, _ := dp.Attributes.Value(attribute.Key("method"))
				st, _ := dp.Attributes.Value(attribute.Key("status"))
				if me.AsString() == method && st.AsString() == status {
					total += dp.Value
				}
			}
		}
	}
	return total
}

// TestLoginHandler_MetricOutcomes drives the Login handler at request level and
// asserts that each exit records exactly the intended method/status label, so a
// missing or misclassified RecordLogin call is caught.
func TestLoginHandler_MetricOutcomes(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	middleware.RebindAuthMeter()
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		middleware.RebindAuthMeter()
		_ = mp.Shutdown(context.Background())
	})

	userRepo := NewMockUserRepository()
	seedUser(t, userRepo, "u1", "alice", "correct-horse", "devops")
	require.NoError(t, userRepo.Create(&models.User{
		ID: "u2", Username: "bob", PasswordHash: hashPassword(t, "pw"), Disabled: true,
	}))
	router, _ := setupAuthRouter(userRepo, false, "", "")

	post := func(body string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	post(`{"username":"ghost","password":"whatever"}`)      // unknown user -> invalid
	post(`{"username":"alice","password":"wrong"}`)         // bad password -> invalid
	post(`{not valid json`)                                 // malformed -> invalid
	post(`{"username":"bob","password":"pw"}`)              // disabled -> disabled
	post(`{"username":"alice","password":"correct-horse"}`) // success

	assert.Equal(t, int64(3), loginStatusCount(t, reader, "local", "invalid"),
		"unknown user + wrong password + malformed payload")
	assert.Equal(t, int64(1), loginStatusCount(t, reader, "local", "disabled"))
	assert.Equal(t, int64(1), loginStatusCount(t, reader, "local", "success"))
	assert.Equal(t, int64(0), loginStatusCount(t, reader, "local", "failure"),
		"no operational failure should be recorded for these inputs")
}
