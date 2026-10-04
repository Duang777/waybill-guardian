//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/platform/tmssandbox"
	"github.com/google/uuid"
)

const crashHelperEnv = "WAYBILL_EFFECT_CRASH_HELPER"

type crashWorkerInput struct {
	TenantID    string                 `json:"tenant_id"`
	BaseURL     string                 `json:"base_url"`
	Phase       string                 `json:"phase"`
	Barrier     string                 `json:"barrier"`
	Command     idempotency.Command    `json:"command"`
	Request     platform.EffectRequest `json:"request"`
	DatabaseURL string                 `json:"database_url"`
}

type processEffectRecord struct {
	requestHash string
	waybillID   domain.WaybillID
	carrierID   domain.CarrierID
}

type processSandbox struct {
	phase       string
	postStarted chan struct{}
	startOnce   sync.Once

	mu           sync.Mutex
	postAttempts int
	accepted     int
	records      map[string]processEffectRecord
}

func TestEffectRecoverySurvivesProcessTermination(t *testing.T) {
	for _, phase := range []string{"before_http", "after_commit", "after_adapter"} {
		t.Run(phase, func(t *testing.T) {
			db := openIntegrationDB(t)
			tenantID := "tenant-" + uuid.NewString()
			sandbox := &processSandbox{
				phase:       phase,
				postStarted: make(chan struct{}),
				records:     make(map[string]processEffectRecord),
			}
			server := httptest.NewServer(http.HandlerFunc(sandbox.handle))
			defer server.Close()

			adapter := newProcessAdapter(t, server.URL, tenantID)
			repository := newProcessRepository(t, db, tenantID, "parent-prepare", adapter)
			runID := domain.RunID(uuid.NewString())
			claimedCtx, command, effect := prepareApprovedReassignEffect(
				t,
				repository,
				runID,
				"CARRIER-SW-42",
			)
			claim := claimFromContext(claimedCtx)
			if err := repository.ReleaseRun(t.Context(), claim); err != nil {
				t.Fatal(err)
			}
			if err := repository.Close(); err != nil {
				t.Fatal(err)
			}

			tempDir := t.TempDir()
			input := crashWorkerInput{
				TenantID:    tenantID,
				BaseURL:     server.URL,
				Phase:       phase,
				Barrier:     filepath.Join(tempDir, "barrier"),
				Command:     command,
				Request:     effect.Request(),
				DatabaseURL: os.Getenv("TEST_DATABASE_URL"),
			}
			inputPath := filepath.Join(tempDir, "input.json")
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(inputPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}

			var output bytes.Buffer
			child := exec.Command(os.Args[0], "-test.run=^TestEffectCrashWorker$", "-test.v")
			child.Env = append(
				os.Environ(),
				crashHelperEnv+"=1",
				"WAYBILL_EFFECT_CRASH_INPUT="+inputPath,
			)
			child.Stdout = &output
			child.Stderr = &output
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			if phase == "after_commit" {
				select {
				case <-sandbox.postStarted:
				case <-time.After(10 * time.Second):
					_ = child.Process.Kill()
					_ = child.Wait()
					t.Fatalf("child did not reach provider commit barrier:\n%s", output.String())
				}
			} else if err := waitForCrashBarrier(input.Barrier, 10*time.Second); err != nil {
				_ = child.Process.Kill()
				_ = child.Wait()
				t.Fatalf("%v:\n%s", err, output.String())
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := child.Wait(); err == nil {
				t.Fatal("crash worker exited successfully instead of being killed")
			}

			time.Sleep(1200 * time.Millisecond)
			recoveryAdapter := newProcessAdapter(t, server.URL, tenantID)
			recovery := newProcessRepository(t, db, tenantID, "parent-recovery", recoveryAdapter)
			defer recovery.Close()
			runCtx, release, err := recovery.AcquireRun(t.Context(), runID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := recovery.Execute(runCtx, effect)
			if err != nil {
				_ = release()
				t.Fatal(err)
			}
			if err := release(); err != nil {
				t.Fatal(err)
			}
			if len(result.Value) == 0 {
				t.Fatal("recovered effect has no result")
			}
			attempts, accepted := sandbox.counts()
			if attempts != 1 || accepted != 1 {
				t.Fatalf(
					"provider mutations = attempts:%d accepted:%d, want 1 and 1",
					attempts,
					accepted,
				)
			}
			state, ok := recovery.Status(command)
			if !ok || state != idempotency.StateSucceeded {
				t.Fatalf("recovered effect state = %q, %v", state, ok)
			}
		})
	}
}

func TestEffectCrashWorker(t *testing.T) {
	if os.Getenv(crashHelperEnv) != "1" {
		t.Skip("crash worker helper")
	}
	raw, err := os.ReadFile(os.Getenv("WAYBILL_EFFECT_CRASH_INPUT"))
	if err != nil {
		t.Fatal(err)
	}
	var input crashWorkerInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	db, err := Open(t.Context(), Config{
		DatabaseURL:    input.DatabaseURL,
		StartupTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adapter := newProcessAdapter(t, input.BaseURL, input.TenantID)
	runtime := platform.WriteRuntime(adapter)
	if input.Phase == "before_http" || input.Phase == "after_adapter" {
		runtime = &crashBarrierRuntime{
			WriteRuntime: adapter,
			phase:        input.Phase,
			barrier:      input.Barrier,
		}
	}
	repository := newProcessRepository(t, db, input.TenantID, "crash-worker", runtime)
	defer repository.Close()
	runCtx, release, err := repository.AcquireRun(t.Context(), input.Command.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = release()
	}()
	effect, err := idempotency.AuthorizeEffect(input.Command, input.Request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Execute(runCtx, effect); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash worker completed before the parent terminated it")
}

type crashBarrierRuntime struct {
	platform.WriteRuntime
	phase   string
	barrier string
}

func (r *crashBarrierRuntime) Dispatch(
	ctx context.Context,
	binding platform.EffectBinding,
	request platform.EffectRequest,
	key domain.IdempotencyKey,
) platform.DispatchResult {
	if r.phase == "before_http" {
		signalCrashBarrier(r.barrier)
		select {}
	}
	result := r.WriteRuntime.Dispatch(ctx, binding, request, key)
	if r.phase == "after_adapter" {
		signalCrashBarrier(r.barrier)
		select {}
	}
	return result
}

func signalCrashBarrier(path string) {
	if err := os.WriteFile(path, []byte("ready"), 0o600); err != nil {
		panic(err)
	}
}

func waitForCrashBarrier(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for crash barrier")
}

func newProcessAdapter(t *testing.T, baseURL string, tenantID string) *tmssandbox.Adapter {
	t.Helper()
	adapter, err := tmssandbox.New(t.Context(), tmssandbox.Config{
		BaseURL:               baseURL,
		Token:                 "process-test-token",
		Account:               tenantID,
		RequestTimeout:        5 * time.Second,
		StartupTimeout:        5 * time.Second,
		ReconciliationHorizon: time.Hour,
		MaxConsistencyWindow:  5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func newProcessRepository(
	t *testing.T,
	db *DB,
	tenantID string,
	workerID string,
	runtime platform.WriteRuntime,
) *Repository {
	t.Helper()
	repository, err := NewRepository(db, RepositoryConfig{
		TenantID:       tenantID,
		WorkerID:       workerID,
		LeaseTTL:       300 * time.Millisecond,
		EffectLeaseTTL: 150 * time.Millisecond,
		OutboxLeaseTTL: time.Second,
		PollInterval:   10 * time.Millisecond,
		WriteRuntime:   runtime,
	})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func (s *processSandbox) handle(response http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodGet &&
		request.URL.Path == "/.well-known/waybill-capabilities":
		writeProcessJSON(response, http.StatusOK, map[string]any{
			"adapter_id":       tmssandbox.AdapterID,
			"contract_version": tmssandbox.ContractVersion,
			"environment":      tmssandbox.Environment,
			"capabilities": []map[string]any{{
				"action":                            domain.ActionReassign,
				"operation":                         "reassignments",
				"key_scope":                         "tenant+operation",
				"key_retention_seconds":             7200,
				"lookup_consistency_window_seconds": 1,
				"same_request_replays":              true,
				"mismatched_request_rejects":        true,
				"lookup_by_key":                     true,
			}},
		})
	case request.Method == http.MethodPost && request.URL.Path == "/v1/reassignments":
		s.handleMutation(response, request)
	case request.Method == http.MethodGet &&
		strings.HasPrefix(request.URL.Path, "/v1/effects/"):
		s.handleLookup(response, request)
	default:
		http.NotFound(response, request)
	}
}

func (s *processSandbox) handleMutation(response http.ResponseWriter, request *http.Request) {
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		writeProcessJSON(response, http.StatusBadRequest, map[string]any{})
		return
	}
	var mutation struct {
		Action    domain.Action    `json:"action"`
		WaybillID domain.WaybillID `json:"waybill_id"`
		CarrierID domain.CarrierID `json:"carrier_id"`
	}
	if json.Unmarshal(raw, &mutation) != nil {
		writeProcessJSON(response, http.StatusBadRequest, map[string]any{})
		return
	}
	key := request.Header.Get("Idempotency-Key")
	requestHash := request.Header.Get("Waybill-Request-SHA256")
	s.mu.Lock()
	s.postAttempts++
	record, exists := s.records[key]
	if !exists {
		record = processEffectRecord{
			requestHash: requestHash,
			waybillID:   mutation.WaybillID,
			carrierID:   mutation.CarrierID,
		}
		s.records[key] = record
		s.accepted++
	}
	s.mu.Unlock()
	s.startOnce.Do(func() {
		close(s.postStarted)
	})
	if s.phase == "after_commit" {
		<-request.Context().Done()
		return
	}
	writeProcessApplied(response, record)
}

func (s *processSandbox) handleLookup(response http.ResponseWriter, request *http.Request) {
	escaped := strings.TrimPrefix(request.URL.Path, "/v1/effects/")
	key, err := url.PathUnescape(escaped)
	if err != nil {
		http.Error(response, "invalid key", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	record, exists := s.records[key]
	s.mu.Unlock()
	if !exists {
		writeProcessJSON(response, http.StatusNotFound, map[string]any{
			"status":         "absent",
			"action":         domain.ActionReassign,
			"request_sha256": request.Header.Get("Waybill-Request-SHA256"),
			"authoritative":  true,
		})
		return
	}
	writeProcessApplied(response, record)
}

func writeProcessApplied(response http.ResponseWriter, record processEffectRecord) {
	writeProcessJSON(response, http.StatusOK, map[string]any{
		"status":         "applied",
		"action":         domain.ActionReassign,
		"request_sha256": record.requestHash,
		"external_ref":   "RA-process-test",
		"request_id":     "request-process-test",
		"result": map[string]any{
			"order_id":   "RA-process-test",
			"waybill_id": record.waybillID,
			"carrier_id": record.carrierID,
			"status":     "accepted",
		},
	})
}

func writeProcessJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func (s *processSandbox) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.postAttempts, s.accepted
}

var _ platform.WriteRuntime = (*crashBarrierRuntime)(nil)
