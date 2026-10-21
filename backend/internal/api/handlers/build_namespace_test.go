package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/validation"
)

// legacyBuildNamespace is a copy of buildNamespace before issue #631. The
// tests use it to show that a namespace that fits in 63 characters does not
// change.
func legacyBuildNamespace(instancePart, ownerPart string) string {
	prefix := "stack-"
	sanitizedOwner := sanitizeRFC1123Label(ownerPart)
	maxInstanceLen := 63 - len(prefix) - 1 - len(sanitizedOwner)
	sanitizedInstance := sanitizeRFC1123Label(instancePart)
	if maxInstanceLen < 1 {
		maxInstanceLen = 1
	}
	if len(sanitizedInstance) > maxInstanceLen {
		sanitizedInstance = sanitizedInstance[:maxInstanceLen]
		sanitizedInstance = strings.TrimRight(sanitizedInstance, "-")
	}
	namespace := fmt.Sprintf("%s%s-%s", prefix, sanitizedInstance, sanitizedOwner)
	if len(namespace) > 63 {
		namespace = namespace[:63]
		namespace = strings.TrimRight(namespace, "-")
	}
	return namespace
}

// assertValidNamespace checks that ns is an RFC 1123 label of at most 63
// characters without a double dash.
func assertValidNamespace(t *testing.T, ns string) {
	t.Helper()
	assert.LessOrEqual(t, len(ns), 63, "namespace %q is too long", ns)
	assert.Empty(t, validation.IsDNS1123Label(ns), "namespace %q is not a DNS label", ns)
	assert.NotContains(t, ns, "--", "namespace %q has a double dash", ns)
	assert.True(t, strings.HasPrefix(ns, "stack-"), "namespace %q has no stack- prefix", ns)
}

func TestBuildNamespace_FittingNameUnchanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		instance string
		owner    string
		want     string
	}{
		{name: "simple", instance: "my-app", owner: "alice", want: "stack-my-app-alice"},
		{name: "sanitized parts", instance: "My_App", owner: "Alice.Smith", want: "stack-my-app-alice-smith"},
		{name: "empty parts", instance: "", owner: "", want: "stack-default-default"},
		{name: "max instance name", instance: strings.Repeat("a", 50), owner: "bob", want: "stack-" + strings.Repeat("a", 50) + "-bob"},
		{name: "exactly 63 characters", instance: strings.Repeat("b", 49), owner: "bob1234", want: "stack-" + strings.Repeat("b", 49) + "-bob1234"},
		{name: "clone name", instance: "checkout-copy-2", owner: "dev-user", want: "stack-checkout-copy-2-dev-user"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := buildNamespace(tt.instance, tt.owner)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, legacyBuildNamespace(tt.instance, tt.owner), got)
			assertValidNamespace(t, got)
		})
	}
}

func TestBuildNamespace_CutNamesDiffer(t *testing.T) {
	t.Parallel()

	base := "feature-payments-refactor"
	names := []string{base, base + "-2", base + "-copy", base + "-copy-2"}

	tests := []struct {
		name  string
		owner string
	}{
		{name: "owner of 40 characters", owner: strings.Repeat("o", 40)},
		{name: "owner of 44 characters", owner: "a-very-long-user-name-from-the-identity-prov"},
		{name: "owner of 30 characters", owner: strings.Repeat("u", 30)},
		{name: "owner of 100 characters", owner: strings.Repeat("w", 100)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			seen := make(map[string]string, len(names))
			legacy := make(map[string]bool, len(names))
			for _, n := range names {
				ns := buildNamespace(n, tt.owner)
				assertValidNamespace(t, ns)
				if other, dup := seen[ns]; dup {
					t.Errorf("names %q and %q both give namespace %q", other, n, ns)
				}
				seen[ns] = n
				legacy[legacyBuildNamespace(n, tt.owner)] = true
			}
			assert.Len(t, seen, len(names))
			// The old rule gave these names one namespace.
			assert.Len(t, legacy, 1)
		})
	}
}

func TestBuildNamespace_CutLayout(t *testing.T) {
	t.Parallel()

	longInstance := "abcdefghi-" + strings.Repeat("x", 40)
	tests := []struct {
		name         string
		instance     string
		owner        string
		wantPrefix   string
		wantSuffix   string
		wantOwnerCut bool
	}{
		{
			name:       "owner stays whole",
			instance:   strings.Repeat("i", 50),
			owner:      "dev-user",
			wantPrefix: "stack-" + strings.Repeat("i", 41) + "-",
			wantSuffix: "-dev-user",
		},
		{
			name:       "owner of 39 characters stays whole",
			instance:   strings.Repeat("i", 30),
			owner:      strings.Repeat("o", 39),
			wantPrefix: "stack-" + strings.Repeat("i", 10) + "-",
			wantSuffix: "-" + strings.Repeat("o", 39),
		},
		{
			name:         "very long owner is cut",
			instance:     strings.Repeat("i", 30),
			owner:        strings.Repeat("o", 80),
			wantPrefix:   "stack-" + strings.Repeat("i", 10) + "-",
			wantSuffix:   "-" + strings.Repeat("o", 39),
			wantOwnerCut: true,
		},
		{
			name:         "short instance with very long owner",
			instance:     "app",
			owner:        strings.Repeat("o", 80),
			wantPrefix:   "stack-app-",
			wantSuffix:   "-" + strings.Repeat("o", 46),
			wantOwnerCut: true,
		},
		{
			name:         "trailing dash of the cut instance is removed",
			instance:     longInstance,
			owner:        strings.Repeat("o", 60),
			wantPrefix:   "stack-abcdefghi-",
			wantSuffix:   "-" + strings.Repeat("o", 39),
			wantOwnerCut: true,
		},
		{
			name:         "trailing dash of the cut owner is removed",
			instance:     strings.Repeat("i", 20),
			owner:        strings.Repeat("o", 38) + "-" + strings.Repeat("p", 20),
			wantPrefix:   "stack-" + strings.Repeat("i", 10) + "-",
			wantSuffix:   "-" + strings.Repeat("o", 38),
			wantOwnerCut: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ns := buildNamespace(tt.instance, tt.owner)
			assertValidNamespace(t, ns)
			assert.True(t, strings.HasPrefix(ns, tt.wantPrefix), "namespace %q, want prefix %q", ns, tt.wantPrefix)
			assert.True(t, strings.HasSuffix(ns, tt.wantSuffix), "namespace %q, want suffix %q", ns, tt.wantSuffix)

			// The middle part is the 6 hex characters of the hash.
			hash := strings.TrimSuffix(strings.TrimPrefix(ns, tt.wantPrefix), tt.wantSuffix)
			assert.Regexp(t, `^[0-9a-f]{6}$`, hash)
			if tt.wantOwnerCut {
				assert.NotContains(t, ns, normalizeRFC1123(tt.owner))
			}
		})
	}
}

func TestBuildNamespace_Deterministic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		instance string
		owner    string
	}{
		{name: "fits", instance: "my-app", owner: "alice"},
		{name: "cut", instance: strings.Repeat("i", 50), owner: strings.Repeat("o", 45)},
		{name: "very long owner", instance: "app", owner: strings.Repeat("o", 200)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			first := buildNamespace(tt.instance, tt.owner)
			for i := 0; i < 5; i++ {
				assert.Equal(t, first, buildNamespace(tt.instance, tt.owner))
			}
		})
	}
}

func TestBuildNamespace_SanitizedInput(t *testing.T) {
	t.Parallel()

	longOwner := "First.Last+Team@Example.Org/" + strings.Repeat("Z", 30)
	tests := []struct {
		name     string
		instance string
		owner    string
		// same is an input pair that sanitizes to the same parts.
		sameInstance string
		sameOwner    string
	}{
		{
			name:         "fits after sanitize",
			instance:     "Feature/Login_Page",
			owner:        "Dev.User",
			sameInstance: "feature-login-page",
			sameOwner:    "dev-user",
		},
		{
			name:         "cut after sanitize",
			instance:     "Feature/ÄÖ__Login--Page..With.A.Long.Name",
			owner:        longOwner,
			sameInstance: "feature-login-page-with-a-long-name",
			sameOwner:    strings.ToLower("first-last-team-example-org-" + strings.Repeat("Z", 30)),
		},
		{
			name:         "only invalid characters",
			instance:     "___",
			owner:        "@@@",
			sameInstance: "default",
			sameOwner:    "default",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ns := buildNamespace(tt.instance, tt.owner)
			assertValidNamespace(t, ns)
			require.Equal(t, normalizeRFC1123(tt.sameInstance), normalizeRFC1123(tt.instance))
			require.Equal(t, normalizeRFC1123(tt.sameOwner), normalizeRFC1123(tt.owner))
			assert.Equal(t, buildNamespace(tt.sameInstance, tt.sameOwner), ns)
		})
	}
}

func TestSanitizeRFC1123Label(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "lowercase", input: "MyApp", want: "myapp"},
		{name: "invalid characters", input: "a_b.c/d", want: "a-b-c-d"},
		{name: "dashes collapse and trim", input: "--a---b--", want: "a-b"},
		{name: "empty", input: "", want: "default"},
		{name: "cut to 63 without trailing dash", input: strings.Repeat("a", 62) + "-b", want: strings.Repeat("a", 62)},
		{name: "cut to 63", input: strings.Repeat("c", 70), want: strings.Repeat("c", 63)},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sanitizeRFC1123Label(tt.input))
		})
	}
}

// longTestOwner is a username that leaves too little room for a whole
// instance name in the namespace.
const longTestOwner = "a-very-long-user-name-from-the-identity-prov"

func TestCreateInstance_LongOwnerCutNamesDiffer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		names []string
	}{
		{name: "numbered names", names: []string{"feature-payments-refactor", "feature-payments-refactor-2"}},
		{name: "copy names", names: []string{"feature-payments-refactor-copy", "feature-payments-refactor-copy-2"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			defRepo := NewMockStackDefinitionRepository()
			seedDefinition(t, defRepo, "d1", "My Def", "uid-1")
			router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), defRepo, NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", longTestOwner, "user")

			seen := make(map[string]bool, len(tt.names))
			for _, n := range tt.names {
				w := httptest.NewRecorder()
				body := fmt.Sprintf(`{"stack_definition_id":"d1","name":%q}`, n)
				req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances", bytes.NewBufferString(body))
				req.Header.Set("Content-Type", "application/json")
				router.ServeHTTP(w, req)

				require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
				var resp models.StackInstance
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, buildNamespace(n, longTestOwner), resp.Namespace)
				assertValidNamespace(t, resp.Namespace)
				assert.False(t, seen[resp.Namespace], "namespace %q is used twice", resp.Namespace)
				seen[resp.Namespace] = true
			}
		})
	}
}

func TestCloneInstance_LongOwnerGeneratedNames(t *testing.T) {
	t.Parallel()

	instRepo := NewMockStackInstanceRepository()
	source := "feature-payments-refactor"
	require.NoError(t, instRepo.Create(context.Background(), &models.StackInstance{
		ID: "i1", StackDefinitionID: "d1", Name: source,
		Namespace: buildNamespace(source, longTestOwner), OwnerID: "uid-1",
		Branch: "master", Status: models.StackStatusRunning,
	}))
	router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", longTestOwner, "user")

	// The old rule gave the source and every clone one namespace, so each
	// clone failed with 409 "no free clone name".
	wantNames := []string{source + "-copy", source + "-copy-2", source + "-copy-3"}
	for _, want := range wantNames {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/stack-instances/i1/clone", nil)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
		var resp models.StackInstance
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.Equal(t, want, resp.Name)
		assert.Equal(t, buildNamespace(want, longTestOwner), resp.Namespace)
		assertValidNamespace(t, resp.Namespace)
	}
}

func TestUpdateInstance_RenameKeepsNamespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		oldName string
		newName string
		owner   string
	}{
		{name: "fitting namespace", oldName: "my-app", newName: "my-renamed-app", owner: "alice"},
		{name: "cut namespace", oldName: "feature-payments-refactor", newName: "feature-payments-refactor-2", owner: longTestOwner},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			instRepo := NewMockStackInstanceRepository()
			ns := buildNamespace(tt.oldName, tt.owner)
			require.NoError(t, instRepo.Create(context.Background(), &models.StackInstance{
				ID: "i1", StackDefinitionID: "d1", Name: tt.oldName,
				Namespace: ns, OwnerID: "uid-1",
				Branch: "master", Status: models.StackStatusRunning,
			}))
			router := setupInstanceRouter(instRepo, NewMockValueOverrideRepository(), NewMockStackDefinitionRepository(), NewMockChartConfigRepository(), NewMockStackTemplateRepository(), NewMockTemplateChartConfigRepository(), "uid-1", tt.owner, "user")

			w := httptest.NewRecorder()
			body := fmt.Sprintf(`{"name":%q}`, tt.newName)
			req, _ := http.NewRequest(http.MethodPut, "/api/v1/stack-instances/i1", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			stored, err := instRepo.FindByID(context.Background(), "i1")
			require.NoError(t, err)
			assert.Equal(t, tt.newName, stored.Name)
			assert.Equal(t, ns, stored.Namespace)
		})
	}
}
