package docs

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/swaggo/swag"
)

// TestReadDocIsValidJSON renders the spec that /swagger/doc.json serves.
// docs.go is a Go template, so a literal template action in an annotation
// (for example a values example with two braces) fails the render, and swag
// then returns the template text without an error.
func TestReadDocIsValidJSON(t *testing.T) {
	t.Parallel()

	doc, err := swag.ReadDoc(SwaggerInfo.InstanceName())
	require.NoError(t, err)

	var spec map[string]any
	assert.NoError(t, json.Unmarshal([]byte(doc), &spec))
	assert.Contains(t, spec, "paths")
}
