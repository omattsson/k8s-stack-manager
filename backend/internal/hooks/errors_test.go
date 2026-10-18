package hooks

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUserMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"denied", &FailedError{Hook: "gate", Err: &DeniedError{Hook: "gate", Message: "image\nmissing"}}, `pre-rollback hook "gate" denied the rollback: image missing`},
		{"wrapped denied", fmt.Errorf("pre-rollback hook: %w", &FailedError{Hook: "gate", Err: &DeniedError{Hook: "gate", Message: "no"}}), `pre-rollback hook "gate" denied the rollback: no`},
		{"transport error hides the URL", &FailedError{Hook: "gate", Err: errors.New("post hook: Post \"http://x/?token=s\": refused")}, `pre-rollback hook "gate" failed (unreachable or timed out)`},
		{"unknown error", errors.New("boom"), "pre-rollback hook failed"},
		{"empty deny message", &FailedError{Hook: "gate", Err: &DeniedError{Hook: "gate"}}, `pre-rollback hook "gate" denied the rollback: subscriber denied`},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, UserMessage(tt.err, EventPreRollback, "rollback"))
		})
	}
	long := UserMessage(&FailedError{Hook: "g", Err: &DeniedError{Message: strings.Repeat("x", 2000)}}, EventPreDeploy, "deployment")
	assert.Less(t, len(long), 600)
}
