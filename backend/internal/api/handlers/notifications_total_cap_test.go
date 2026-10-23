package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xeipuuv/gojsonschema"
)

// TestNotificationHandler_List_TotalCap checks the capped total (#654):
// total_capped in the JSON, and that unread_only skips the CountUnread query
// when the total is exact.
func TestNotificationHandler_List_TotalCap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		query           string
		totalCap        int
		unread, read    int
		wantTotal       int64
		wantCapped      bool
		wantUnreadCount int64
		wantCount       int
		wantCountCalls  int
	}{
		{
			name: "below the cap is exact", query: "", totalCap: 10, unread: 3, read: 2,
			wantTotal: 5, wantCapped: false, wantUnreadCount: 3, wantCount: 5, wantCountCalls: 1,
		},
		{
			name: "above the cap is capped", query: "?limit=2", totalCap: 4, unread: 3, read: 3,
			wantTotal: 4, wantCapped: true, wantUnreadCount: 3, wantCount: 2, wantCountCalls: 1,
		},
		{
			name: "capped total counts from the offset", query: "?limit=2&offset=1", totalCap: 4, unread: 3, read: 3,
			wantTotal: 5, wantCapped: true, wantUnreadCount: 3, wantCount: 2, wantCountCalls: 1,
		},
		{
			name: "unread_only not capped uses the total as unread_count", query: "?unread_only=true", totalCap: 10, unread: 3, read: 4,
			wantTotal: 3, wantCapped: false, wantUnreadCount: 3, wantCount: 3, wantCountCalls: 0,
		},
		{
			name: "unread_only capped counts the unread notifications", query: "?unread_only=true&limit=1", totalCap: 2, unread: 5, read: 1,
			wantTotal: 2, wantCapped: true, wantUnreadCount: 5, wantCount: 1, wantCountCalls: 1,
		},
		{
			name: "offset past the end is exact", query: "?offset=50", totalCap: 10, unread: 2, read: 1,
			wantTotal: 3, wantCapped: false, wantUnreadCount: 2, wantCount: 0, wantCountCalls: 1,
		},
		{
			name: "huge offset", query: "?offset=9223372036854775807", totalCap: 10, unread: 1, read: 1,
			wantTotal: 2, wantCapped: false, wantUnreadCount: 1, wantCount: 0, wantCountCalls: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationRouter()
			repo.SetTotalCap(tt.totalCap)
			for i := 0; i < tt.unread+tt.read; i++ {
				seedNotification(t, repo, fmt.Sprintf("n%d", i), "user-1", "deployment.success", "t", i >= tt.unread)
			}
			seedNotification(t, repo, "other", "user-2", "deployment.success", "t", false)

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/notifications"+tt.query, nil)
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			res, err := gojsonschema.Validate(gojsonschema.NewStringLoader(notificationListSchema), gojsonschema.NewBytesLoader(w.Body.Bytes()))
			require.NoError(t, err)
			assert.True(t, res.Valid(), "schema errors: %v", res.Errors())

			var body struct {
				Notifications []json.RawMessage `json:"notifications"`
				Total         int64             `json:"total"`
				UnreadCount   int64             `json:"unread_count"`
				TotalCapped   *bool             `json:"total_capped"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.NotNil(t, body.TotalCapped, "total_capped is always present")
			assert.Equal(t, tt.wantCapped, *body.TotalCapped)
			assert.Equal(t, tt.wantTotal, body.Total)
			assert.Equal(t, tt.wantUnreadCount, body.UnreadCount)
			assert.Len(t, body.Notifications, tt.wantCount)
			assert.Equal(t, tt.wantCountCalls, repo.CountUnreadCalls(), "CountUnread calls")
		})
	}
}

// TestNotificationHandler_List_CountUnreadError checks that a failed
// CountUnread still gives 500 "Internal server error", also on the
// unread_only path when the total is capped.
func TestNotificationHandler_List_CountUnreadError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		query      string
		totalCap   int
		wantStatus int
	}{
		{name: "all notifications", query: "", totalCap: 10, wantStatus: http.StatusInternalServerError},
		{name: "unread_only capped", query: "?unread_only=true", totalCap: 1, wantStatus: http.StatusInternalServerError},
		{name: "unread_only exact skips the query", query: "?unread_only=true", totalCap: 10, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			router, repo := setupNotificationRouter()
			repo.SetTotalCap(tt.totalCap)
			seedNotification(t, repo, "n1", "user-1", "deployment.success", "t", false)
			seedNotification(t, repo, "n2", "user-1", "deployment.success", "t", false)
			repo.SetCountUnreadError(errors.New("count failed: secret detail"))

			w := httptest.NewRecorder()
			req, _ := http.NewRequest(http.MethodGet, "/api/v1/notifications"+tt.query, nil)
			router.ServeHTTP(w, req)

			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantStatus == http.StatusInternalServerError {
				var body map[string]string
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Equal(t, "Internal server error", body["error"])
				assert.NotContains(t, w.Body.String(), "secret detail")
			}
		})
	}
}
