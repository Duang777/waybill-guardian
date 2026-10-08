package agent

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/agents/agentstate"
)

func TestOnlineCompatibleAPIsPauseResumeDelayDamageAndLossWaybills(t *testing.T) {
	loaded, err := filestore.Load("../../data/simulated/waybills-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		waybillID domain.WaybillID
		writes    int
	}{
		{waybillID: "YD2026100001", writes: 3},
		{waybillID: "YD2026100007", writes: 4},
		{waybillID: "YD2026100013", writes: 4},
	}

	for _, apiStyle := range []string{APIStyleResponses, APIStyleChatCompletions} {
		t.Run(apiStyle, func(t *testing.T) {
			for index, test := range cases {
				t.Run(string(test.waybillID), func(t *testing.T) {
					dataDir := t.TempDir()
					fake := newCompatibleModelServer(apiStyle)
					server := httptest.NewServer(fake)
					defer server.Close()

					journal, err := audit.Open(dataDir+"/audit", time.Now)
					if err != nil {
						t.Fatal(err)
					}
					defer journal.Close()
					approvals, err := approval.NewStore(journal, time.Now)
					if err != nil {
						t.Fatal(err)
					}
					fixture, err := guardtools.NewFixtureWriteRuntime(loaded.Reads)
					if err != nil {
						t.Fatal(err)
					}
					observedRuntime := newObservedWriteRuntime(fixture)
					effects, err := idempotency.NewStore(
						journal,
						idempotency.StoreConfig{Runtime: observedRuntime},
					)
					if err != nil {
						t.Fatal(err)
					}
					handlers, err := guardtools.NewHandlers(loaded.Reads)
					if err != nil {
						t.Fatal(err)
					}
					registry, err := guardtools.NewRegistry(handlers)
					if err != nil {
						t.Fatal(err)
					}
					engine, err := NewEngine(
						dataDir+"/history",
						registry,
						[]agents.Middleware{
							NewAuditMiddleware(journal),
							NewReadBindingMiddleware(loaded.Reads),
							NewWriteEffectMiddleware(approvals, effects, registry),
						},
						0,
						ModelConfig{
							Mode:     ModeOnline,
							APIStyle: apiStyle,
							BaseURL:  server.URL + "/v1",
							APIKey:   "test-key",
							Model:    "compatible-model",
						},
					)
					if err != nil {
						t.Fatal(err)
					}
					defer engine.Close()

					runContext := domain.RunContext{
						RunID: domain.RunID(fmt.Sprintf(
							"run-%s-%d",
							apiStyle,
							index,
						)),
						IncidentID:  domain.IncidentID("incident-" + string(test.waybillID)),
						WaybillID:   test.waybillID,
						PlanVersion: 1,
					}
					paused, err := engine.Start(t.Context(), runContext)
					if err != nil {
						t.Fatalf("start %s: %v", test.waybillID, err)
					}
					assertOnlineAcceptancePaused(
						t,
						paused,
						test.waybillID,
						test.writes,
					)
					assertOnlineAcceptanceNoWrites(t, fixture, observedRuntime)
					assertOnlineAcceptanceAudit(t, journal, runContext.RunID, 5, nil)
					if requests := fake.requestCount(test.waybillID); requests != 5 {
						t.Fatalf(
							"%s model requests before approval = %d, want 5",
							test.waybillID,
							requests,
						)
					}

					confirmed := confirmOnlineAcceptanceApproval(
						t,
						approvals,
						registry,
						runContext,
						paused,
					)
					completed, err := engine.Resume(
						t.Context(),
						runContext,
						paused.SDKRunID,
						paused.Interrupts,
						true,
					)
					if err != nil {
						t.Fatalf("resume %s: %v", test.waybillID, err)
					}
					if completed.Status != agentstate.RunStatusCompleted {
						t.Fatalf(
							"%s resumed status = %q, want completed; text=%q",
							test.waybillID,
							completed.Status,
							completed.Text,
						)
					}
					if completed.Text != compatibleTerminalText {
						t.Fatalf(
							"%s completed text = %q, want %q",
							test.waybillID,
							completed.Text,
							compatibleTerminalText,
						)
					}
					if requests := fake.requestCount(test.waybillID); requests != 6 {
						t.Fatalf(
							"%s model requests after resume = %d, want 6",
							test.waybillID,
							requests,
						)
					}
					assertOnlineAcceptanceDispatches(t, observedRuntime, confirmed.Items)
					assertOnlineAcceptanceWriteCounts(t, fixture, confirmed.Items)
					assertOnlineAcceptanceAudit(
						t,
						journal,
						runContext.RunID,
						6,
						confirmed.Items,
					)
					callIDs := make([]string, 0, len(confirmed.Items))
					for _, item := range confirmed.Items {
						callIDs = append(callIDs, item.CallID)
					}
					if !fake.sawResults(test.waybillID, callIDs) {
						t.Fatalf(
							"%s provider did not receive every write result: %v",
							test.waybillID,
							callIDs,
						)
					}
				})
			}
		})
	}
}

func assertOnlineAcceptancePaused(
	t *testing.T,
	outcome Outcome,
	waybillID domain.WaybillID,
	expectedWrites int,
) {
	t.Helper()
	if outcome.Status != agentstate.RunStatusPaused {
		t.Fatalf("%s status = %q, want paused", waybillID, outcome.Status)
	}
	if outcome.SDKRunID == "" {
		t.Fatalf("%s paused outcome has no SDK run ID", waybillID)
	}
	if outcome.Proposal == nil || outcome.Proposal.Digest == "" {
		t.Fatalf("%s proposal = %+v", waybillID, outcome.Proposal)
	}
	if len(outcome.Interrupts) != expectedWrites {
		t.Fatalf(
			"%s interrupts = %d, want %d",
			waybillID,
			len(outcome.Interrupts),
			expectedWrites,
		)
	}
	callIDs := make(map[string]struct{}, len(outcome.Interrupts))
	for _, interrupt := range outcome.Interrupts {
		if interrupt.CallID == "" {
			t.Fatalf("%s write %s has no call ID", waybillID, interrupt.WireName)
		}
		if _, duplicate := callIDs[interrupt.CallID]; duplicate {
			t.Fatalf("%s has duplicate call ID %q", waybillID, interrupt.CallID)
		}
		callIDs[interrupt.CallID] = struct{}{}
		var arguments struct {
			WaybillID string `json:"waybill_id"`
		}
		if err := json.Unmarshal(interrupt.Arguments, &arguments); err != nil {
			t.Fatal(err)
		}
		if arguments.WaybillID != string(waybillID) {
			t.Fatalf(
				"%s write %s targets %q",
				waybillID,
				interrupt.WireName,
				arguments.WaybillID,
			)
		}
	}
}

func assertOnlineAcceptanceNoWrites(
	t *testing.T,
	fixture *guardtools.FixtureWriteRuntime,
	observed *observedWriteRuntime,
) {
	t.Helper()
	if dispatches := observed.snapshot(); len(dispatches) != 0 {
		t.Fatalf("platform dispatched %d writes before approval", len(dispatches))
	}
	for _, action := range []domain.Action{
		domain.ActionReassign,
		domain.ActionCreateClaim,
		domain.ActionSendSMS,
	} {
		if count := fixture.WriteCount(action); count != 0 {
			t.Fatalf("%s writes before approval = %d", action, count)
		}
	}
}

func confirmOnlineAcceptanceApproval(
	t *testing.T,
	approvals *approval.Store,
	registry *guardtools.Registry,
	runContext domain.RunContext,
	outcome Outcome,
) approval.Approval {
	t.Helper()
	items := make([]approval.Item, 0, len(outcome.Interrupts))
	callIDs := make([]string, 0, len(outcome.Interrupts))
	for _, interrupt := range outcome.Interrupts {
		write, err := registry.ParseActiveWrite(interrupt.WireName, interrupt.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		if write.Action != interrupt.Action {
			t.Fatalf(
				"interrupt %s action = %q, want %q",
				interrupt.CallID,
				interrupt.Action,
				write.Action,
			)
		}
		if validationErr := write.ValidateRunContext(runContext); validationErr != nil {
			t.Fatal(validationErr)
		}
		if write.LegacyKey != "" {
			t.Fatalf("interrupt %s supplied an idempotency key", interrupt.CallID)
		}
		identity, err := idempotency.Derive(idempotency.DerivationInput{
			RunContext: runContext,
			Action:     write.Action,
			Target:     write.Target,
			Arguments:  write.Arguments,
		})
		if err != nil {
			t.Fatal(err)
		}
		callIDs = append(callIDs, interrupt.CallID)
		items = append(items, approval.Item{
			CallID:          interrupt.CallID,
			Action:          write.Action,
			WireName:        write.WireName,
			Params:          write.Arguments,
			ArgumentsHash:   identity.ArgumentsHash,
			IdentityVersion: identity.Version,
			EffectID:        identity.EffectID,
			IdempotencyKey:  identity.Key,
		})
	}
	approvalID := approval.IDForPlan(
		runContext.RunID,
		runContext.PlanVersion,
		callIDs,
	)
	if _, err := approvals.Create(t.Context(), approval.Approval{
		ID:          approvalID,
		RunID:       runContext.RunID,
		SDKRunID:    outcome.SDKRunID,
		WaybillID:   runContext.WaybillID,
		PlanVersion: runContext.PlanVersion,
		Items:       items,
		Reason:      outcome.Proposal.Summary,
	}); err != nil {
		t.Fatal(err)
	}
	confirmed, err := approvals.Decide(t.Context(), approvalID, approval.Decision{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "online-acceptance-reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != approval.StatusConfirmed {
		t.Fatalf("approval status = %q, want confirmed", confirmed.Status)
	}
	return confirmed
}

func assertOnlineAcceptanceDispatches(
	t *testing.T,
	observed *observedWriteRuntime,
	items []approval.Item,
) {
	t.Helper()
	dispatches := observed.snapshot()
	if len(dispatches) != len(items) {
		t.Fatalf("platform dispatches = %d, want %d", len(dispatches), len(items))
	}
	byKey := make(map[domain.IdempotencyKey]observedDispatch, len(dispatches))
	for _, dispatch := range dispatches {
		if _, duplicate := byKey[dispatch.key]; duplicate {
			t.Fatalf("platform dispatched key %q more than once", dispatch.key)
		}
		byKey[dispatch.key] = dispatch
	}
	for _, item := range items {
		dispatch, ok := byKey[item.IdempotencyKey]
		if !ok {
			t.Fatalf(
				"approved call %s key %q was not dispatched",
				item.CallID,
				item.IdempotencyKey,
			)
		}
		if dispatch.request.Action != item.Action ||
			dispatch.binding.Action != item.Action {
			t.Fatalf(
				"call %s dispatched action request=%q binding=%q, want %q",
				item.CallID,
				dispatch.request.Action,
				dispatch.binding.Action,
				item.Action,
			)
		}
		if dispatch.request.ArgumentsHash != item.ArgumentsHash {
			t.Fatalf(
				"call %s arguments hash = %q, want %q",
				item.CallID,
				dispatch.request.ArgumentsHash,
				item.ArgumentsHash,
			)
		}
		if !sameCompatibleJSON(dispatch.request.Arguments, item.Params) {
			t.Fatalf(
				"call %s arguments = %s, want %s",
				item.CallID,
				dispatch.request.Arguments,
				item.Params,
			)
		}
		if dispatch.result.Disposition != platform.EffectSucceeded {
			t.Fatalf(
				"call %s disposition = %q, want succeeded",
				item.CallID,
				dispatch.result.Disposition,
			)
		}
	}
}

func assertOnlineAcceptanceWriteCounts(
	t *testing.T,
	fixture *guardtools.FixtureWriteRuntime,
	items []approval.Item,
) {
	t.Helper()
	expected := make(map[domain.Action]int)
	for _, item := range items {
		expected[item.Action]++
	}
	for _, action := range []domain.Action{
		domain.ActionReassign,
		domain.ActionCreateClaim,
		domain.ActionSendSMS,
	} {
		if count := fixture.WriteCount(action); count != expected[action] {
			t.Fatalf(
				"%s fixture writes = %d, want %d",
				action,
				count,
				expected[action],
			)
		}
	}
}

func assertOnlineAcceptanceAudit(
	t *testing.T,
	journal audit.Journal,
	runID domain.RunID,
	expectedModelCalls int,
	items []approval.Item,
) {
	t.Helper()
	events, err := journal.Replay(t.Context(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	readResults := make(map[domain.Action]bool)
	writeToolResults := make(map[string]int)
	writeStarted := make(map[string]int)
	writeExecuted := make(map[string]int)
	modelCalls := 0
	for _, event := range events {
		switch event.Type {
		case audit.EventToolResult:
			var payload struct {
				CallID string        `json:"call_id"`
				Action domain.Action `json:"action"`
				Error  string        `json:"error"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Action.IsWrite() {
				if payload.Error != "" {
					t.Fatalf(
						"write call %s tool result failed: %s",
						payload.CallID,
						payload.Error,
					)
				}
				writeToolResults[payload.CallID]++
			} else if payload.Error == "" {
				readResults[payload.Action] = true
			}
		case audit.EventModelCallFinished:
			var payload modelCallFinished
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Mode != ModeOnline || payload.Model != "compatible-model" {
				t.Fatalf("model audit = %+v", payload)
			}
			if payload.Usage == nil || payload.Usage.TotalTokens != 50 {
				t.Fatalf("model usage = %+v", payload.Usage)
			}
			modelCalls++
		case audit.EventWriteStarted:
			callID := auditCallID(t, event)
			writeStarted[callID]++
		case audit.EventWriteExecuted:
			callID := auditCallID(t, event)
			writeExecuted[callID]++
		case audit.EventWriteFailed, audit.EventWriteUnknown,
			audit.EventDuplicateSuppressed:
			t.Fatalf("run %s has unexpected %s event: %s", runID, event.Type, event.Payload)
		}
	}
	for _, action := range []domain.Action{
		domain.ActionGetWaybill,
		domain.ActionGetTracking,
		domain.ActionGetDriver,
		domain.ActionGetRoadWeather,
	} {
		if !readResults[action] {
			t.Fatalf("run %s has no successful %s result", runID, action)
		}
	}
	if modelCalls != expectedModelCalls {
		t.Fatalf(
			"run %s model audit events = %d, want %d",
			runID,
			modelCalls,
			expectedModelCalls,
		)
	}
	if len(items) == 0 {
		if len(writeToolResults) != 0 ||
			len(writeStarted) != 0 ||
			len(writeExecuted) != 0 {
			t.Fatalf(
				"run %s recorded writes before approval: tool=%v started=%v executed=%v",
				runID,
				writeToolResults,
				writeStarted,
				writeExecuted,
			)
		}
		return
	}
	if len(writeToolResults) != len(items) ||
		len(writeStarted) != len(items) ||
		len(writeExecuted) != len(items) {
		t.Fatalf(
			"run %s write audit cardinality: tool=%v started=%v executed=%v",
			runID,
			writeToolResults,
			writeStarted,
			writeExecuted,
		)
	}
	for _, item := range items {
		if writeToolResults[item.CallID] != 1 ||
			writeStarted[item.CallID] != 1 ||
			writeExecuted[item.CallID] != 1 {
			t.Fatalf(
				"call %s audit counts: tool=%d started=%d executed=%d",
				item.CallID,
				writeToolResults[item.CallID],
				writeStarted[item.CallID],
				writeExecuted[item.CallID],
			)
		}
	}
}

func auditCallID(t *testing.T, event audit.Event) string {
	t.Helper()
	var payload struct {
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CallID == "" {
		t.Fatalf("%s event has no call_id: %s", event.Type, event.Payload)
	}
	return payload.CallID
}
