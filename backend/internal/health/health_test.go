package health

import (
	"context"
	"errors"
	"testing"
)

func TestNew(t *testing.T) {
	t.Parallel()
	h := New()
	if h == nil {
		t.Error("Expected non-nil HealthChecker")
	}
}

func TestLivenessCheck(t *testing.T) {
	t.Parallel()
	h := New()
	status := h.CheckLiveness(context.Background())

	if status.Status != "UP" {
		t.Errorf("Expected status UP, got %s", status.Status)
	}

	if status.Uptime == "" {
		t.Error("Expected non-empty uptime")
	}
}

func TestReadinessCheck(t *testing.T) {
	t.Parallel()

	t.Run("Service not ready", func(t *testing.T) {
		t.Parallel()
		h := New() // Create a fresh instance for this subtest
		status := h.CheckReadiness(context.Background())
		if status.Status != "DOWN" {
			t.Errorf("Expected status DOWN, got %s", status.Status)
		}
	})

	t.Run("Service ready", func(t *testing.T) {
		t.Parallel()
		h := New() // Create a fresh instance for this subtest
		h.SetReady(true)
		status := h.CheckReadiness(context.Background())
		if status.Status != "UP" {
			t.Errorf("Expected status UP, got %s", status.Status)
		}
	})
}

func TestHealthChecks(t *testing.T) {
	t.Parallel()

	t.Run("All checks passing", func(t *testing.T) {
		t.Parallel()
		h := New() // Create a fresh instance for this subtest
		h.SetReady(true)
		h.AddCheck("test", func(_ context.Context) error { return nil })
		status := h.CheckReadiness(context.Background())
		if status.Status != "UP" {
			t.Errorf("Expected status UP, got %s", status.Status)
		}
		if status.Checks["test"].Status != "UP" {
			t.Errorf("Expected check status UP, got %s", status.Checks["test"].Status)
		}
	})

	t.Run("Failed check", func(t *testing.T) {
		t.Parallel()
		h := New() // Create a fresh instance for this subtest
		h.SetReady(true)
		h.AddCheck("failing", func(_ context.Context) error { return errors.New("test error") })
		status := h.CheckReadiness(context.Background())
		if status.Status != "DOWN" {
			t.Errorf("Expected status DOWN, got %s", status.Status)
		}
		if status.Checks["failing"].Status != "DOWN" {
			t.Errorf("Expected check status DOWN, got %s", status.Checks["failing"].Status)
		}
	})
}

func TestOptionalCheckDoesNotFailReadiness(t *testing.T) {
	t.Parallel()

	h := New()
	h.SetReady(true)
	h.AddCheck("database", func(context.Context) error { return nil })
	h.AddOptionalCheck("git_provider", func(context.Context) error { return errors.New("unreachable") })

	st := h.CheckReadiness(context.Background())
	if st.Status != "UP" {
		t.Errorf("an optional check must not mark the service DOWN, got %q", st.Status)
	}
	if got := st.Checks["git_provider"]; got.Status != "DOWN" || got.Message != "unreachable" {
		t.Errorf("git_provider check = %+v, want DOWN with message", got)
	}

	h.AddCheck("helm", func(context.Context) error { return errors.New("missing") })
	if got := h.CheckReadiness(context.Background()).Status; got != "DOWN" {
		t.Errorf("a required check must still mark DOWN, got %q", got)
	}
}
