package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"backend/internal/cluster"
	"backend/internal/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fakeResolver is a clusterIDResolver with a fixed answer.
type fakeResolver struct {
	id  string
	err error
}

func (f fakeResolver) ResolveClusterID(clusterID string) (string, error) {
	if clusterID != "" {
		return clusterID, nil
	}
	return f.id, f.err
}

func TestLoadSharedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		clusterID string
		resolver  clusterIDResolver
		nilRepo   bool
		failList  bool
		want      []string
		wantErr   bool
	}{
		{name: "instance cluster, sorted by priority, blank skipped", clusterID: "cl-1", want: []string{"p0: a\n", "p5: b\n"}},
		{name: "default cluster through resolver", resolver: fakeResolver{id: "cl-1"}, want: []string{"p0: a\n", "p5: b\n"}},
		{name: "no default cluster", resolver: fakeResolver{err: cluster.ErrNoDefaultCluster}, want: nil},
		{name: "no default cluster, wrapped", resolver: fakeResolver{err: fmt.Errorf("%w: x", cluster.ErrNoDefaultCluster)}, want: nil},
		{name: "resolver failure fails closed", resolver: fakeResolver{err: errors.New("db down")}, wantErr: true},
		{name: "list failure fails closed", clusterID: "cl-1", failList: true, wantErr: true},
		{name: "repository not wired", clusterID: "cl-1", nilRepo: true, want: nil},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mem := newMockSharedValuesRepo()
			require.NoError(t, mem.Create(&models.SharedValues{ID: "b", ClusterID: "cl-1", Priority: 5, Values: "p5: b\n"}))
			require.NoError(t, mem.Create(&models.SharedValues{ID: "a", ClusterID: "cl-1", Priority: 0, Values: "p0: a\n"}))
			require.NoError(t, mem.Create(&models.SharedValues{ID: "blank", ClusterID: "cl-1", Priority: 3, Values: "  \n"}))

			var repo models.SharedValuesRepository = mem
			if tt.failList {
				repo = &failingSharedValuesRepo{mem}
			}
			if tt.nilRepo {
				repo = nil
			}

			got, err := loadSharedValues(repo, tt.resolver, nil, &models.StackInstance{ID: "i1", ClusterID: tt.clusterID})
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestLoadSharedValues_OrderAndInvalidYAML checks the tie-breakers for equal
// priorities (name, then ID) and that an entry with invalid YAML is skipped
// (with a warning) while the others still apply.
func TestLoadSharedValues_OrderAndInvalidYAML(t *testing.T) {
	t.Parallel()

	mem := newMockSharedValuesRepo()
	for _, sv := range []*models.SharedValues{
		{ID: "id-c", ClusterID: "cl-1", Name: "beta", Priority: 5, Values: "from: beta-c\n"},
		{ID: "id-b", ClusterID: "cl-1", Name: "alpha", Priority: 5, Values: "from: alpha-b\n"},
		{ID: "id-a", ClusterID: "cl-1", Name: "beta", Priority: 5, Values: "from: beta-a\n"},
		{ID: "id-d", ClusterID: "cl-1", Name: "zeta", Priority: 1, Values: "from: zeta-d\n"},
		{ID: "id-bad", ClusterID: "cl-1", Name: "broken", Priority: 2, Values: "key: [unclosed\n"},
		{ID: "id-list", ClusterID: "cl-1", Name: "list", Priority: 3, Values: "- a\n- b\n"},
	} {
		require.NoError(t, mem.Create(sv))
	}

	got, err := loadSharedValues(mem, nil, nil, &models.StackInstance{ID: "i1", ClusterID: "cl-1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"from: zeta-d\n", "from: alpha-b\n", "from: beta-a\n", "from: beta-c\n"}, got)
}

// TestWarnInvalidSharedValues checks that the skip warning names the entry,
// does not log value content from the YAML error, and is logged once per
// entry version.
func TestWarnInvalidSharedValues(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	sv := &models.SharedValues{ID: "sv-warn-test-1", Name: "broken", ClusterID: "cl-1", UpdatedAt: time.Unix(100, 0),
		Values: "password: s3cr3t-value\n- oops\n"}
	var parsed map[string]any
	yamlErr := yaml.Unmarshal([]byte(sv.Values), &parsed)
	require.Error(t, yamlErr)

	warnInvalidSharedValuesTo(logger, "cl-1", sv, yamlErr)
	warnInvalidSharedValuesTo(logger, "cl-1", sv, yamlErr)

	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "skipping shared values with invalid YAML"), "logged once per entry version")
	assert.Contains(t, out, "shared_values_id=sv-warn-test-1")
	assert.Contains(t, out, "shared_values_name=broken")
	assert.Contains(t, out, "cluster_id=cl-1")
	assert.Contains(t, out, "yaml_error_line=")
	assert.NotContains(t, out, "s3cr3t")

	// A new version of the entry is reported again.
	sv.UpdatedAt = time.Unix(200, 0)
	warnInvalidSharedValuesTo(logger, "cl-1", sv, yamlErr)
	assert.Equal(t, 2, strings.Count(buf.String(), "skipping shared values with invalid YAML"))
}
