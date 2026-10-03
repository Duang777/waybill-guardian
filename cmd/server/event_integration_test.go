//go:build integration

package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/httpauth"
	"github.com/Duang777/waybill-guardian/internal/storage/postgres"
	"github.com/google/uuid"
)

func TestEventIngressConcurrentReplayWithPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	db, err := postgres.Open(t.Context(), postgres.Config{
		DatabaseURL:    databaseURL,
		StartupTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenantID := "http-integration-" + uuid.NewString()
	repository, err := postgres.NewRepository(db, postgres.RepositoryConfig{
		TenantID:       tenantID,
		WorkerID:       "http-worker",
		LeaseTTL:       5 * time.Second,
		OutboxLeaseTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	access, err := httpauth.New(httpauth.Config{
		Mode:     httpauth.ModeLocal,
		TenantID: httpauth.TenantID(tenantID),
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(newHandlerWithEvents(nil, access, repository, nil))
	defer server.Close()

	eventID := "event-" + uuid.NewString()
	body := localEventBody(time.Now().UTC(), eventID)
	const requests = 10
	responses := make(chan eventHTTPResponse, requests)
	var wait sync.WaitGroup
	wait.Add(requests)
	for range requests {
		go func() {
			defer wait.Done()
			request, requestErr := http.NewRequest(
				http.MethodPost,
				server.URL+"/v1/events",
				bytes.NewReader(body),
			)
			if requestErr != nil {
				responses <- eventHTTPResponse{err: requestErr}
				return
			}
			request.Header.Set("Content-Type", "application/cloudevents+json")
			response, requestErr := server.Client().Do(request)
			if requestErr != nil {
				responses <- eventHTTPResponse{err: requestErr}
				return
			}
			responseBody, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			responses <- eventHTTPResponse{
				status:   response.StatusCode,
				body:     responseBody,
				replayed: response.Header.Get("Idempotent-Replayed") == "true",
				err:      errors.Join(readErr, closeErr),
			}
		}()
	}
	wait.Wait()
	close(responses)

	var canonical []byte
	replayed := 0
	for response := range responses {
		if response.err != nil {
			t.Fatal(response.err)
		}
		if response.status != http.StatusAccepted {
			t.Fatalf("status = %d, want 202; body=%s", response.status, response.body)
		}
		if canonical == nil {
			canonical = response.body
		} else if !bytes.Equal(canonical, response.body) {
			t.Fatalf("response body changed:\n%s\n%s", canonical, response.body)
		}
		if response.replayed {
			replayed++
		}
	}
	if replayed != requests-1 {
		t.Fatalf("replayed responses = %d, want %d", replayed, requests-1)
	}
	stats, err := repository.OutboxStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 ||
		stats.Publishing != 0 ||
		stats.RetryableFailed != 0 ||
		stats.PermanentFailed != 0 {
		t.Fatalf("outbox stats = %+v", stats)
	}
	claims, err := repository.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Event().AggregateType != "incident" {
		t.Fatalf("outbox claims = %+v", claims)
	}
}

type eventHTTPResponse struct {
	status   int
	body     []byte
	replayed bool
	err      error
}
