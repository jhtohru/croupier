package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHealthLive(t *testing.T) {
	// Deliberately no Deps at all: liveness must never depend on anything,
	// not even a Ready func being set.
	srv := NewServer(Deps{})
	rec := doRequest(t, srv, http.MethodGet, "/health/live", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHealthReady(t *testing.T) {
	t.Run("no Ready func configured defaults to ok", func(t *testing.T) {
		srv := NewServer(Deps{})
		rec := doRequest(t, srv, http.MethodGet, "/health/ready", nil)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("dependency up", func(t *testing.T) {
		srv := NewServer(Deps{Ready: func(ctx context.Context) error { return nil }})
		rec := doRequest(t, srv, http.MethodGet, "/health/ready", nil)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("dependency down", func(t *testing.T) {
		srv := NewServer(Deps{Ready: func(ctx context.Context) error { return errors.New("postgres unreachable") }})
		rec := doRequest(t, srv, http.MethodGet, "/health/ready", nil)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}
