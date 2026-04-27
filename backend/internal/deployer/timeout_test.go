package deployer

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsTimeoutError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error returns false",
			err:      nil,
			expected: false,
		},
		{
			name:     "context.DeadlineExceeded returns true",
			err:      context.DeadlineExceeded,
			expected: true,
		},
		{
			name:     "wrapped context.DeadlineExceeded returns true",
			err:      fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
			expected: true,
		},
		{
			name:     "error containing timed out returns true",
			err:      errors.New("request timed out waiting for pods"),
			expected: true,
		},
		{
			name:     "error containing deadline exceeded returns true",
			err:      errors.New("context deadline exceeded"),
			expected: true,
		},
		{
			name:     "connection refused returns false",
			err:      errors.New("connection refused"),
			expected: false,
		},
		{
			name:     "unrelated error returns false",
			err:      errors.New("some other error"),
			expected: false,
		},
		{
			name:     "empty error message returns false",
			err:      errors.New(""),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := isTimeoutError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
