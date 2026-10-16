package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/database"
	"backend/internal/helm"
	"backend/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- Helpers of the TTL and stop-time rules ----

func TestMaxTTLMinutesFromDays(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		days int
		want int
	}{
		{"zero gives the default", 0, MaxTTLMinutes},
		{"negative gives the default", -3, MaxTTLMinutes},
		{"one day", 1, 1440},
		{"seven days", 7, 10080},
		{"one year", 365, 525600},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, maxTTLMinutesFromDays(tt.days))
		})
	}
}

func TestTTLExceedsMessage(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "TTL must not exceed 43200 minutes (30 days)", ttlExceedsMessage(MaxTTLMinutes))
	assert.Equal(t, "TTL must not exceed 1440 minutes (1 day)", ttlExceedsMessage(1440))
}

func TestStopTimeError(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * 60
	tests := []struct {
		name string
		stop time.Time
		want string
	}{
		{"in one hour", now.Add(time.Hour), ""},
		{"exactly at the limit", now.Add(7 * 24 * time.Hour), ""},
		{"now is not in the future", now, msgStopTimeNotNext},
		{"in the past", now.Add(-time.Minute), msgStopTimeNotNext},
		{"after the limit", now.Add(7*24*time.Hour + time.Second), "expires_at must not be later than 7 days from now"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, stopTimeError(tt.stop, now, week))
		})
	}
}

func TestStopTimePassed(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	assert.True(t, stopTimePassed(0, &past, now), "fixed stop time in the past")
	assert.True(t, stopTimePassed(0, &now, now), "fixed stop time equal to now")
	assert.False(t, stopTimePassed(0, &future, now), "fixed stop time in the future")
	assert.False(t, stopTimePassed(60, &past, now), "a TTL instance restarts its TTL on deploy")
	assert.False(t, stopTimePassed(0, nil, now), "no expiry")
}

func TestRequestedExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	stop := now.Add(48 * time.Hour)
	stopOtherZone := stop.In(time.FixedZone("CEST", 2*60*60))
	past := now.Add(-time.Hour)
	tooLate := now.Add(8 * 24 * time.Hour)
	week := 7 * 24 * 60

	tests := []struct {
		name       string
		ttl        *int
		expiresAt  *time.Time
		defaultTTL int
		wantTTL    int
		wantExpiry *time.Time
		wantMsg    string
	}{
		{name: "both omitted without default: no expiry"},
		{name: "both omitted: default TTL", defaultTTL: 480, wantTTL: 480, wantExpiry: ptrTime(now.Add(480 * time.Minute))},
		{name: "explicit 0: no expiry, no default", ttl: intPtr(0), defaultTTL: 480},
		{name: "TTL", ttl: intPtr(120), wantTTL: 120, wantExpiry: ptrTime(now.Add(2 * time.Hour))},
		{name: "TTL at the limit", ttl: intPtr(week), wantTTL: week, wantExpiry: ptrTime(now.Add(7 * 24 * time.Hour))},
		{name: "TTL above the limit", ttl: intPtr(week + 1), wantMsg: "TTL must not exceed 10080 minutes (7 days)"},
		{name: "negative TTL", ttl: intPtr(-1), wantMsg: "ttl_minutes must be non-negative"},
		{name: "default TTL above the limit", defaultTTL: week + 1, wantMsg: "TTL must not exceed 10080 minutes (7 days)"},
		{name: "stop time: TTL 0, expiry set, default ignored", expiresAt: &stop, defaultTTL: 480, wantExpiry: &stop},
		{name: "stop time in another zone is stored as UTC", expiresAt: &stopOtherZone, wantExpiry: &stop},
		{name: "stop time with TTL", ttl: intPtr(60), expiresAt: &stop, wantMsg: msgTTLAndStopTime},
		{name: "stop time with explicit TTL 0", ttl: intPtr(0), expiresAt: &stop, wantMsg: msgTTLAndStopTime},
		{name: "stop time in the past", expiresAt: &past, wantMsg: msgStopTimeNotNext},
		{name: "stop time after the limit", expiresAt: &tooLate, wantMsg: "expires_at must not be later than 7 days from now"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ttl, expiry, msg := requestedExpiry(tt.ttl, tt.expiresAt, tt.defaultTTL, week, now)
			assert.Equal(t, tt.wantMsg, msg)
			assert.Equal(t, tt.wantTTL, ttl)
			if tt.wantExpiry == nil {
				assert.Nil(t, expiry)
				return
			}
			require.NotNil(t, expiry)
			assert.True(t, tt.wantExpiry.Equal(*expiry), "expiry %v, want %v", expiry, tt.wantExpiry)
			assert.Equal(t, time.UTC, expiry.Location())
		})
	}
}

func TestExtendExpiry_ConfiguredLimit(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	week := 7 * 24 * 60
	inSixDays := now.Add(6 * 24 * time.Hour)
	got := extendExpiry(&inSixDays, 3*24*60, week, now)
	require.NotNil(t, got)
	assert.WithinDuration(t, now.Add(7*24*time.Hour), *got, time.Second, "capped at now plus the limit")

	inTenDays := now.Add(10 * 24 * time.Hour) // set when the limit was larger
	got = extendExpiry(&inTenDays, 60, week, now)
	require.NotNil(t, got)
	assert.WithinDuration(t, inTenDays, *got, time.Second, "an extend never shortens the expiry")
}

func TestWithMaxTTLDays(t *testing.T) {
	t.Parallel()
	h := (&InstanceHandler{}).WithMaxTTLDays(7)
	assert.Equal(t, 10080, h.maxTTL())
	assert.Equal(t, MaxTTLMinutes, (&InstanceHandler{}).maxTTL(), "not set: 30 days")
	assert.Equal(t, MaxTTLMinutes, (&InstanceHandler{}).WithMaxTTLDays(0).maxTTL())
	q := (&QuickDeployHandler{}).WithMaxTTLDays(14)
	assert.Equal(t, 14*24*60, q.maxTTLMinutes)
}

func ptrTime(t time.Time) *time.Time { return &t }

// ---- Handler tests ----

// ttlTestEnv is an instance handler with a deploy manager, a 7-day limit
// (MAX_INSTANCE_TTL_DAYS=7) and the TTL routes.
type ttlTestEnv struct {
	router  *gin.Engine
	instRep *MockStackInstanceRepository
}

func newTTLTestEnv(t *testing.T, defaultTTL int) *ttlTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	instRepo := NewMockStackInstanceRepository()
	defRepo := NewMockStackDefinitionRepository()
	ccRepo := NewMockChartConfigRepository()
	logRepo := NewMockDeploymentLogRepository()
	overrideRepo := NewMockValueOverrideRepository()
	boRepo := NewMockChartBranchOverrideRepository()
	seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
	require.NoError(t, ccRepo.Create(&models.ChartConfig{
		ID: "c1", StackDefinitionID: "d1", ChartName: "nginx", RepositoryURL: "oci://example.com/charts/nginx", DeployOrder: 1,
	}))

	h, err := NewInstanceHandlerWithDeployer(
		instRepo, overrideRepo, boRepo, defRepo, ccRepo,
		NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), helm.NewValuesGenerator(), NewMockUserRepository(),
		newBulkTestManager(instRepo, logRepo), nil, nil, logRepo, nil,
		defaultTTL,
		&mockHandlerTxRunner{repos: database.TxRepos{
			StackInstance:  instRepo,
			ValueOverride:  overrideRepo,
			BranchOverride: boRepo,
		}},
	)
	require.NoError(t, err)
	h.WithMaxTTLDays(7)

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", "uid-1")
		c.Set("username", "alice")
		c.Set("role", "devops")
		c.Next()
	})
	insts := r.Group("/api/v1/stack-instances")
	insts.POST("", h.CreateInstance)
	insts.PUT("/:id", h.UpdateInstance)
	insts.POST("/:id/clone", h.CloneInstance)
	insts.POST("/:id/extend", h.ExtendTTL)
	insts.POST("/:id/deploy", h.DeployInstance)
	insts.POST("/bulk/deploy", h.BulkDeploy)
	return &ttlTestEnv{router: r, instRep: instRepo}
}

func (e *ttlTestEnv) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req, _ := http.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// seed stores an instance with the given status, TTL and expiry.
func (e *ttlTestEnv) seed(t *testing.T, id, status string, ttl int, expiresAt *time.Time) {
	t.Helper()
	inst := seedInstance(t, e.instRep, id, "stack-"+id, "d1", "uid-1", status)
	inst.TTLMinutes = ttl
	inst.ExpiresAt = expiresAt
	require.NoError(t, e.instRep.Update(inst))
}

func (e *ttlTestEnv) get(t *testing.T, id string) *models.StackInstance {
	t.Helper()
	inst, err := e.instRep.FindByID(id)
	require.NoError(t, err)
	return inst
}

func errorOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	msg, _ := resp["error"].(string)
	return msg
}

func rfc3339(t time.Time) string { return t.Format(time.RFC3339) }

func TestCreateInstance_StopTime(t *testing.T) {
	t.Parallel()
	inTwoDays := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	tests := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantError  string
		check      func(t *testing.T, inst models.StackInstance)
	}{
		{
			name: "expires_at sets a fixed stop time; the default TTL does not apply",
			body: map[string]any{"name": "fixed", "stack_definition_id": "d1",
				"expires_at": inTwoDays.In(time.FixedZone("CEST", 7200)).Format(time.RFC3339)},
			wantStatus: http.StatusCreated,
			check: func(t *testing.T, inst models.StackInstance) {
				assert.Equal(t, 0, inst.TTLMinutes)
				require.NotNil(t, inst.ExpiresAt)
				assert.True(t, inTwoDays.Equal(*inst.ExpiresAt))
			},
		},
		{
			name:       "expires_at with ttl_minutes",
			body:       map[string]any{"name": "both", "stack_definition_id": "d1", "ttl_minutes": 60, "expires_at": rfc3339(inTwoDays)},
			wantStatus: http.StatusBadRequest, wantError: msgTTLAndStopTime,
		},
		{
			name:       "expires_at in the past",
			body:       map[string]any{"name": "past", "stack_definition_id": "d1", "expires_at": rfc3339(time.Now().Add(-time.Hour))},
			wantStatus: http.StatusBadRequest, wantError: msgStopTimeNotNext,
		},
		{
			name:       "expires_at after the configured limit",
			body:       map[string]any{"name": "late", "stack_definition_id": "d1", "expires_at": rfc3339(time.Now().Add(8 * 24 * time.Hour))},
			wantStatus: http.StatusBadRequest, wantError: "expires_at must not be later than 7 days from now",
		},
		{
			name:       "TTL above the configured limit",
			body:       map[string]any{"name": "big", "stack_definition_id": "d1", "ttl_minutes": 7*24*60 + 1},
			wantStatus: http.StatusBadRequest, wantError: "TTL must not exceed 10080 minutes (7 days)",
		},
		{
			name:       "expires_at without a time zone is invalid",
			body:       map[string]any{"name": "nozone", "stack_definition_id": "d1", "expires_at": "2026-10-17T18:00:00"},
			wantStatus: http.StatusBadRequest, wantError: msgInvalidRequestFormat,
		},
		{
			name:       "the default TTL applies without ttl_minutes and expires_at",
			body:       map[string]any{"name": "dflt", "stack_definition_id": "d1"},
			wantStatus: http.StatusCreated,
			check: func(t *testing.T, inst models.StackInstance) {
				assert.Equal(t, 480, inst.TTLMinutes)
				require.NotNil(t, inst.ExpiresAt)
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newTTLTestEnv(t, 480)
			w := env.do(t, http.MethodPost, "/api/v1/stack-instances", tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				assert.Equal(t, tt.wantError, errorOf(t, w))
			}
			if tt.check != nil {
				var inst models.StackInstance
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &inst))
				tt.check(t, inst)
			}
		})
	}
}

func TestUpdateInstance_StopTime(t *testing.T) {
	t.Parallel()
	inTwoDays := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)

	t.Run("expires_at sets a fixed stop time", func(t *testing.T) {
		t.Parallel()
		env := newTTLTestEnv(t, 0)
		env.seed(t, "i1", models.StackStatusDraft, 120, ptrTime(time.Now().Add(2*time.Hour)))
		w := env.do(t, http.MethodPut, "/api/v1/stack-instances/i1", map[string]any{"expires_at": rfc3339(inTwoDays)})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		got := env.get(t, "i1")
		assert.Equal(t, 0, got.TTLMinutes)
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, inTwoDays.Equal(*got.ExpiresAt))
	})

	t.Run("ttl_minutes replaces a fixed stop time", func(t *testing.T) {
		t.Parallel()
		env := newTTLTestEnv(t, 0)
		env.seed(t, "i1", models.StackStatusDraft, 0, &inTwoDays)
		w := env.do(t, http.MethodPut, "/api/v1/stack-instances/i1", map[string]any{"ttl_minutes": 60})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		got := env.get(t, "i1")
		assert.Equal(t, 60, got.TTLMinutes)
		require.NotNil(t, got.ExpiresAt)
		assert.WithinDuration(t, time.Now().Add(time.Hour), *got.ExpiresAt, time.Minute)
	})

	t.Run("an update without TTL fields keeps the stop time", func(t *testing.T) {
		t.Parallel()
		env := newTTLTestEnv(t, 0)
		env.seed(t, "i1", models.StackStatusDraft, 0, &inTwoDays)
		w := env.do(t, http.MethodPut, "/api/v1/stack-instances/i1", map[string]any{"branch": "main"})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		got := env.get(t, "i1")
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, inTwoDays.Equal(*got.ExpiresAt))
	})

	t.Run("an echoed expires_at with ttl_minutes (whole instance sent back) uses the TTL", func(t *testing.T) {
		t.Parallel()
		env := newTTLTestEnv(t, 0)
		stored := time.Now().UTC().Add(3 * time.Hour)
		env.seed(t, "i1", models.StackStatusRunning, 240, &stored)
		w := env.do(t, http.MethodPut, "/api/v1/stack-instances/i1",
			map[string]any{"name": "stack-i1", "ttl_minutes": 0, "expires_at": stored.Format(time.RFC3339Nano)})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		got := env.get(t, "i1")
		assert.Equal(t, 0, got.TTLMinutes)
		assert.Nil(t, got.ExpiresAt, "ttl_minutes 0 clears the expiry")
	})

	t.Run("a changed expires_at with ttl_minutes gives 400", func(t *testing.T) {
		t.Parallel()
		env := newTTLTestEnv(t, 0)
		stored := time.Now().UTC().Add(3 * time.Hour)
		env.seed(t, "i1", models.StackStatusRunning, 240, &stored)
		w := env.do(t, http.MethodPut, "/api/v1/stack-instances/i1",
			map[string]any{"ttl_minutes": 60, "expires_at": rfc3339(stored.Add(24 * time.Hour))})
		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, msgTTLAndStopTime, errorOf(t, w))
	})

	t.Run("expires_at after the limit", func(t *testing.T) {
		t.Parallel()
		env := newTTLTestEnv(t, 0)
		env.seed(t, "i1", models.StackStatusDraft, 0, nil)
		w := env.do(t, http.MethodPut, "/api/v1/stack-instances/i1",
			map[string]any{"expires_at": rfc3339(time.Now().Add(10 * 24 * time.Hour))})
		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, "expires_at must not be later than 7 days from now", errorOf(t, w))
	})
}

func TestCloneInstance_StopTime(t *testing.T) {
	t.Parallel()
	inTwoDays := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	past := time.Now().UTC().Add(-time.Hour)
	inOneDay := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	tests := []struct {
		name       string
		srcTTL     int
		srcExpiry  *time.Time
		body       map[string]any
		wantStatus int
		wantTTL    int
		wantExpiry *time.Time
		wantNoExp  bool
	}{
		{name: "copies a future fixed stop time", srcExpiry: &inTwoDays, body: map[string]any{"name": "c1"},
			wantStatus: http.StatusCreated, wantExpiry: &inTwoDays},
		{name: "a passed fixed stop time is not copied", srcExpiry: &past, body: map[string]any{"name": "c2"},
			wantStatus: http.StatusCreated, wantNoExp: true},
		{name: "expires_at of the request", srcTTL: 60, srcExpiry: ptrTime(time.Now().Add(time.Hour)),
			body: map[string]any{"name": "c3", "expires_at": rfc3339(inOneDay)}, wantStatus: http.StatusCreated, wantExpiry: &inOneDay},
		{name: "copies the TTL of the source", srcTTL: 120, body: map[string]any{"name": "c4"},
			wantStatus: http.StatusCreated, wantTTL: 120},
		{name: "expires_at with ttl_minutes", body: map[string]any{"name": "c5", "ttl_minutes": 30, "expires_at": rfc3339(inOneDay)},
			wantStatus: http.StatusBadRequest},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newTTLTestEnv(t, 0)
			env.seed(t, "src", models.StackStatusRunning, tt.srcTTL, tt.srcExpiry)
			w := env.do(t, http.MethodPost, "/api/v1/stack-instances/src/clone", tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantStatus != http.StatusCreated {
				return
			}
			var resp struct {
				ID         string     `json:"id"`
				TTLMinutes int        `json:"ttl_minutes"`
				ExpiresAt  *time.Time `json:"expires_at"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			got := env.get(t, resp.ID)
			assert.Equal(t, tt.wantTTL, got.TTLMinutes)
			switch {
			case tt.wantNoExp:
				assert.Nil(t, got.ExpiresAt)
			case tt.wantExpiry != nil:
				require.NotNil(t, got.ExpiresAt)
				assert.True(t, tt.wantExpiry.Equal(*got.ExpiresAt), "expiry %v, want %v", got.ExpiresAt, tt.wantExpiry)
			}
		})
	}
}

func TestDeployInstance_StopTime(t *testing.T) {
	t.Parallel()
	inTwoDays := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)

	t.Run("a passed stop time gives 409", func(t *testing.T) {
		t.Parallel()
		env := newTTLTestEnv(t, 0)
		env.seed(t, "i1", models.StackStatusDraft, 0, ptrTime(time.Now().UTC().Add(-time.Minute)))
		w := env.do(t, http.MethodPost, "/api/v1/stack-instances/i1/deploy", nil)
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		assert.Equal(t, msgStopTimePassed, errorOf(t, w))
		assert.Equal(t, models.StackStatusDraft, env.get(t, "i1").Status)
	})

	t.Run("a future stop time stays after the deploy", func(t *testing.T) {
		t.Parallel()
		env := newTTLTestEnv(t, 0)
		env.seed(t, "i1", models.StackStatusDraft, 0, &inTwoDays)
		w := env.do(t, http.MethodPost, "/api/v1/stack-instances/i1/deploy", nil)
		require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
		got := env.get(t, "i1")
		assert.Equal(t, 0, got.TTLMinutes)
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, inTwoDays.Equal(*got.ExpiresAt))
	})

	t.Run("bulk deploy refuses a passed stop time", func(t *testing.T) {
		t.Parallel()
		env := newTTLTestEnv(t, 0)
		env.seed(t, "i1", models.StackStatusStopped, 0, ptrTime(time.Now().UTC().Add(-time.Minute)))
		env.seed(t, "i2", models.StackStatusStopped, 0, &inTwoDays)
		w := env.do(t, http.MethodPost, "/api/v1/stack-instances/bulk/deploy", BulkOperationRequest{InstanceIDs: []string{"i1", "i2"}})
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var resp BulkOperationResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, 1, resp.Succeeded)
		assert.Equal(t, 1, resp.Failed)
		for _, r := range resp.Results {
			if r.InstanceID == "i1" {
				assert.Equal(t, "error", r.Status)
				assert.Equal(t, msgStopTimePassed, r.Error)
			}
		}
		got := env.get(t, "i2")
		require.NotNil(t, got.ExpiresAt)
		assert.True(t, inTwoDays.Equal(*got.ExpiresAt), "bulk deploy keeps a fixed stop time")
	})
}

func TestExtendTTL_Until(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	inOneHour := now.Add(time.Hour)
	inThreeDays := now.Add(72 * time.Hour).Truncate(time.Second)
	tests := []struct {
		name       string
		ttl        int
		expiresAt  *time.Time
		body       map[string]any
		wantStatus int
		wantError  string
		wantExpiry *time.Time
	}{
		{name: "sets the expiry of a TTL instance", ttl: 60, expiresAt: &inOneHour,
			body: map[string]any{"until": rfc3339(inThreeDays)}, wantStatus: http.StatusOK, wantExpiry: &inThreeDays},
		{name: "sets the expiry of a fixed stop time", expiresAt: &inOneHour,
			body: map[string]any{"until": rfc3339(inThreeDays)}, wantStatus: http.StatusOK, wantExpiry: &inThreeDays},
		{name: "after a passed expiry, until only needs to be in the future", ttl: 60, expiresAt: ptrTime(now.Add(-time.Hour)),
			body: map[string]any{"until": rfc3339(inThreeDays)}, wantStatus: http.StatusOK, wantExpiry: &inThreeDays},
		{name: "until before the current expiry", ttl: 60, expiresAt: &inThreeDays,
			body: map[string]any{"until": rfc3339(now.Add(2 * time.Hour))}, wantStatus: http.StatusBadRequest,
			wantError: "until must be after the current expiry"},
		{name: "until after the limit", ttl: 60, expiresAt: &inOneHour,
			body: map[string]any{"until": rfc3339(now.Add(8 * 24 * time.Hour))}, wantStatus: http.StatusBadRequest,
			wantError: "until must not be later than 7 days from now"},
		{name: "no expiry: nothing to extend",
			body: map[string]any{"until": rfc3339(inThreeDays)}, wantStatus: http.StatusBadRequest,
			wantError: "Instance has no expiry; nothing to extend"},
		{name: "until with minutes", ttl: 60, expiresAt: &inOneHour,
			body: map[string]any{"until": rfc3339(inThreeDays), "minutes": 30}, wantStatus: http.StatusBadRequest,
			wantError: "Send either until, minutes or ttl_minutes"},
		{name: "minutes are capped at now plus the limit", ttl: 60, expiresAt: ptrTime(now.Add(6 * 24 * time.Hour)),
			body: map[string]any{"minutes": 3 * 24 * 60}, wantStatus: http.StatusOK, wantExpiry: ptrTime(now.Add(7 * 24 * time.Hour))},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newTTLTestEnv(t, 0)
			env.seed(t, "i1", models.StackStatusRunning, tt.ttl, tt.expiresAt)
			w := env.do(t, http.MethodPost, "/api/v1/stack-instances/i1/extend", tt.body)
			require.Equal(t, tt.wantStatus, w.Code, w.Body.String())
			if tt.wantError != "" {
				assert.Equal(t, tt.wantError, errorOf(t, w))
				return
			}
			got := env.get(t, "i1")
			assert.Equal(t, tt.ttl, got.TTLMinutes, "an extend never changes ttl_minutes")
			require.NotNil(t, got.ExpiresAt)
			assert.WithinDuration(t, *tt.wantExpiry, *got.ExpiresAt, 2*time.Second, fmt.Sprint(got.ExpiresAt))
		})
	}
}
