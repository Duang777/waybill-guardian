package proposal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
)

func TestCompilerAcceptsAuditedScalarCitations(t *testing.T) {
	store := openTestJournal(t)
	runID := domain.RunID("run-proposal")
	appendToolResult(t, store, runID, "call-waybill", domain.ActionGetWaybill, map[string]any{
		"waybill_id": "YD2026101001",
		"candidate_carriers": []any{
			map[string]any{"carrier_id": "CARRIER-SW-42", "name": "西南速运"},
		},
	}, "")
	appendToolResult(t, store, runID, "call-tracking", domain.ActionGetTracking, map[string]any{
		"points": []any{
			map[string]any{"label": "杭州", "anomaly": false},
			map[string]any{"label": "绵阳北服务区", "anomaly": true, "stop_hours": 6},
		},
	}, "")
	driverEvent := appendToolResult(
		t,
		store,
		runID,
		"call-driver",
		domain.ActionGetDriver,
		map[string]any{
			"driver_id":              "DRV-0286",
			"continuous_drive_hours": 9,
			"fatigue_alert":          true,
		},
		"",
	)

	compiler, err := NewCompiler(store)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := compiler.Compile(t.Context(), runID, mustMarshal(t, validDraft()))
	if err != nil {
		t.Fatal(err)
	}
	if accepted.SchemaVersion != SchemaVersion || accepted.ConfidenceBPS != 8600 {
		t.Fatalf("accepted proposal = %+v", accepted)
	}
	if accepted.Digest == "" {
		t.Fatal("accepted proposal has no digest")
	}
	if len(accepted.Attribution) != 2 || len(accepted.Attribution[0].Evidence) != 2 {
		t.Fatalf("attribution = %+v", accepted.Attribution)
	}
	citation := accepted.Attribution[0].Evidence[0]
	if citation.ToolCallID != "call-driver" ||
		citation.FieldPath != "/continuous_drive_hours" ||
		string(citation.Value) != "9" ||
		citation.DisplayValue != "9" ||
		citation.SourceEventID != driverEvent.EventID ||
		citation.SourceSeq != driverEvent.Seq ||
		citation.SourceHash != driverEvent.Hash {
		t.Fatalf("citation = %+v", citation)
	}
	if len(accepted.Alternatives) != 1 ||
		accepted.Alternatives[0].CarrierID != "CARRIER-SW-42" {
		t.Fatalf("alternatives = %+v", accepted.Alternatives)
	}

	replayed, err := compiler.Compile(t.Context(), runID, mustMarshal(t, validDraft()))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Digest != accepted.Digest {
		t.Fatalf("digest changed: %q != %q", replayed.Digest, accepted.Digest)
	}
}

func TestDecodeRejectsInvalidProposalShape(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		code IssueCode
	}{
		{
			name: "unknown field",
			raw:  strings.Replace(string(mustMarshal(t, validDraft())), `"summary":`, `"extra":true,"summary":`, 1),
			code: IssueInvalidJSON,
		},
		{
			name: "duplicate field",
			raw:  strings.Replace(string(mustMarshal(t, validDraft())), `"summary":`, `"summary":"first","summary":`, 1),
			code: IssueInvalidJSON,
		},
		{
			name: "trailing value",
			raw:  string(mustMarshal(t, validDraft())) + `{}`,
			code: IssueInvalidJSON,
		},
		{
			name: "wrong version",
			raw:  string(mustMarshal(t, mutateDraft(func(draft *Draft) { draft.SchemaVersion = "proposal.v2" }))),
			code: IssueInvalidSchema,
		},
		{
			name: "missing attribution",
			raw:  string(mustMarshal(t, mutateDraft(func(draft *Draft) { draft.Attribution = nil }))),
			code: IssueInvalidSchema,
		},
		{
			name: "invalid confidence",
			raw:  string(mustMarshal(t, mutateDraft(func(draft *Draft) { draft.ConfidenceBPS = 10001 }))),
			code: IssueInvalidSchema,
		},
		{
			name: "invalid pointer escape",
			raw: string(mustMarshal(t, mutateDraft(func(draft *Draft) {
				draft.Attribution[0].EvidenceRefs[0].FieldPath = "/bad~2path"
			}))),
			code: IssueInvalidSchema,
		},
		{
			name: "object quoted value",
			raw: string(mustMarshal(t, mutateDraft(func(draft *Draft) {
				draft.Attribution[0].EvidenceRefs[0].Quoted = json.RawMessage(`{"hours":9}`)
			}))),
			code: IssueInvalidSchema,
		},
		{
			name: "available impact unsupported",
			raw: string(mustMarshal(t, mutateDraft(func(draft *Draft) {
				value := 60.0
				draft.ExpectedImpact.ETASavedMin = ImpactMetricDraft{
					Availability: AvailabilityAvailable,
					Value:        &value,
					EvidenceRefs: []EvidenceRef{{
						ToolCallID: "call-driver",
						FieldPath:  "/continuous_drive_hours",
						Quoted:     json.RawMessage("9"),
					}},
				}
			}))),
			code: IssueUnsupportedImpact,
		},
		{
			name: "unavailable impact with value",
			raw: string(mustMarshal(t, mutateDraft(func(draft *Draft) {
				value := 60.0
				draft.ExpectedImpact.ETASavedMin.Value = &value
			}))),
			code: IssueInvalidSchema,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode([]byte(test.raw))
			if err == nil {
				t.Fatal("Decode accepted invalid proposal")
			}
			if got := CodeOf(err); got != test.code {
				t.Fatalf("issue code = %q, want %q; err = %v", got, test.code, err)
			}
		})
	}
}

func TestCompilerRejectsUntrustedCitations(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *audit.Store, domain.RunID)
		mutate  func(*Draft)
		want    string
	}{
		{
			name: "cross run",
			prepare: func(t *testing.T, store *audit.Store, runID domain.RunID) {
				appendToolResult(t, store, "another-run", "call-driver", domain.ActionGetDriver,
					map[string]any{"continuous_drive_hours": 9, "fatigue_alert": true}, "")
				appendNote(t, store, runID)
			},
			want: "has no result in this run",
		},
		{
			name: "write tool",
			prepare: func(t *testing.T, store *audit.Store, runID domain.RunID) {
				appendToolResult(t, store, runID, "call-driver", domain.ActionReassign,
					map[string]any{"continuous_drive_hours": 9, "fatigue_alert": true}, "")
			},
			want: "is not a read tool",
		},
		{
			name: "failed read",
			prepare: func(t *testing.T, store *audit.Store, runID domain.RunID) {
				appendToolResult(t, store, runID, "call-driver", domain.ActionGetDriver, nil, "timeout")
			},
			want: "did not produce a successful result",
		},
		{
			name: "duplicate call id",
			prepare: func(t *testing.T, store *audit.Store, runID domain.RunID) {
				appendToolResult(t, store, runID, "call-driver", domain.ActionGetDriver,
					map[string]any{"continuous_drive_hours": 9, "fatigue_alert": true}, "")
				appendToolResultWithEventID(t, store, runID, "duplicate-result", "call-driver",
					domain.ActionGetDriver,
					map[string]any{"continuous_drive_hours": 9, "fatigue_alert": true}, "")
			},
			want: "duplicate result events",
		},
		{
			name:    "missing field",
			prepare: appendValidDriver,
			mutate: func(draft *Draft) {
				draft.Attribution[0].EvidenceRefs[0].FieldPath = "/missing"
			},
			want: "does not exist",
		},
		{
			name:    "non scalar field",
			prepare: appendValidDriver,
			mutate: func(draft *Draft) {
				draft.Attribution[0].EvidenceRefs[0].FieldPath = ""
				draft.Attribution[0].EvidenceRefs[0].Quoted = json.RawMessage(`"ignored"`)
			},
			want: "must resolve to a JSON scalar",
		},
		{
			name:    "quoted value mismatch",
			prepare: appendValidDriver,
			mutate: func(draft *Draft) {
				draft.Attribution[0].EvidenceRefs[0].Quoted = json.RawMessage("8")
			},
			want: "does not match audited value",
		},
		{
			name: "invalid array index",
			prepare: func(t *testing.T, store *audit.Store, runID domain.RunID) {
				appendToolResult(t, store, runID, "call-driver", domain.ActionGetDriver,
					map[string]any{
						"continuous_drive_hours": 9,
						"fatigue_alert":          true,
						"values":                 []any{"one"},
					}, "")
			},
			mutate: func(draft *Draft) {
				draft.Attribution[0].EvidenceRefs[0].FieldPath = "/values/01"
				draft.Attribution[0].EvidenceRefs[0].Quoted = json.RawMessage(`"one"`)
			},
			want: "invalid array index",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestJournal(t)
			runID := domain.RunID("run-untrusted")
			test.prepare(t, store, runID)
			draft := validDraft()
			draft.Alternatives = nil
			if test.mutate != nil {
				test.mutate(&draft)
			}
			compiler, err := NewCompiler(store)
			if err != nil {
				t.Fatal(err)
			}
			_, err = compiler.Compile(t.Context(), runID, mustMarshal(t, draft))
			if err == nil || CodeOf(err) != IssueInvalidCitation ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Compile error = %v, want invalid citation containing %q", err, test.want)
			}
		})
	}
}

func TestCompilerResolvesEscapedJSONPointer(t *testing.T) {
	store := openTestJournal(t)
	runID := domain.RunID("run-pointer")
	appendToolResult(t, store, runID, "call-driver", domain.ActionGetDriver, map[string]any{
		"a/b": map[string]any{"~key": "verified"},
	}, "")
	draft := validDraft()
	draft.Alternatives = nil
	draft.Attribution = []AttributionDraft{{
		Factor:        "转义字段证据",
		ConfidenceBPS: 8000,
		EvidenceRefs: []EvidenceRef{{
			ToolCallID: "call-driver",
			FieldPath:  "/a~1b/~0key",
			Quoted:     json.RawMessage(`"verified"`),
		}},
	}}
	compiler, err := NewCompiler(store)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := compiler.Compile(t.Context(), runID, mustMarshal(t, draft))
	if err != nil {
		t.Fatal(err)
	}
	if got := accepted.Attribution[0].Evidence[0].DisplayValue; got != "verified" {
		t.Fatalf("display value = %q", got)
	}
}

func TestCompilerRejectsCarrierOutsideAuditedCandidates(t *testing.T) {
	store := openTestJournal(t)
	runID := domain.RunID("run-carrier")
	appendValidDriver(t, store, runID)
	appendToolResult(t, store, runID, "call-waybill", domain.ActionGetWaybill, map[string]any{
		"candidate_carriers": []any{
			map[string]any{"carrier_id": "CARRIER-ALLOWED"},
		},
	}, "")

	draft := validDraft()
	draft.Attribution = draft.Attribution[:1]
	draft.Alternatives[0].CarrierID = "CARRIER-HALLUCINATED"
	compiler, err := NewCompiler(store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = compiler.Compile(t.Context(), runID, mustMarshal(t, draft))
	if err == nil || CodeOf(err) != IssueInvalidAlternative ||
		!strings.Contains(err.Error(), "not present in audited waybill candidates") {
		t.Fatalf("Compile error = %v", err)
	}
}

func validDraft() Draft {
	return Draft{
		SchemaVersion: SchemaVersion,
		Summary:       "疲劳驾驶和异常停留共同造成延误风险。",
		ConfidenceBPS: 8600,
		Attribution: []AttributionDraft{
			{
				Factor:        "司机连续驾驶时间过长",
				ConfidenceBPS: 9100,
				EvidenceRefs: []EvidenceRef{
					{
						ToolCallID: "call-driver",
						FieldPath:  "/continuous_drive_hours",
						Quoted:     json.RawMessage("9"),
					},
					{
						ToolCallID: "call-driver",
						FieldPath:  "/fatigue_alert",
						Quoted:     json.RawMessage("true"),
					},
				},
			},
			{
				Factor:        "服务区长时间停留",
				ConfidenceBPS: 8800,
				EvidenceRefs: []EvidenceRef{{
					ToolCallID: "call-tracking",
					FieldPath:  "/points/1/label",
					Quoted:     json.RawMessage(`"绵阳北服务区"`),
				}},
			},
		},
		Alternatives: []Alternative{{
			CarrierID: "CARRIER-SW-42",
			Reason:    "候选运力中时效和履约率更优",
		}},
		ExpectedImpact: ExpectedImpactDraft{
			ETASavedMin: ImpactMetricDraft{
				Availability: AvailabilityUnavailable,
				Reason:       "当前证据没有改派后的到达时间",
			},
			CostDeltaCNY: ImpactMetricDraft{
				Availability: AvailabilityUnavailable,
				Reason:       "当前证据没有成本字段",
			},
		},
	}
}

func mutateDraft(mutate func(*Draft)) Draft {
	draft := validDraft()
	mutate(&draft)
	return draft
}

func openTestJournal(t *testing.T) *audit.Store {
	t.Helper()
	store, err := audit.Open(t.TempDir(), func() time.Time {
		return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func appendValidDriver(t *testing.T, store *audit.Store, runID domain.RunID) {
	t.Helper()
	appendToolResult(t, store, runID, "call-driver", domain.ActionGetDriver, map[string]any{
		"continuous_drive_hours": 9,
		"fatigue_alert":          true,
	}, "")
}

func appendToolResult(
	t *testing.T,
	store *audit.Store,
	runID domain.RunID,
	callID string,
	action domain.Action,
	result any,
	callErr string,
) audit.Event {
	t.Helper()
	return appendToolResultWithEventID(
		t,
		store,
		runID,
		"tool:"+callID+":result",
		callID,
		action,
		result,
		callErr,
	)
}

func appendToolResultWithEventID(
	t *testing.T,
	store *audit.Store,
	runID domain.RunID,
	eventID string,
	callID string,
	action domain.Action,
	result any,
	callErr string,
) audit.Event {
	t.Helper()
	payload := map[string]any{
		"call_id": callID,
		"action":  action,
	}
	if result != nil {
		payload["result"] = result
	}
	if callErr != "" {
		payload["error"] = callErr
	}
	event, err := store.Append(context.Background(), runID, audit.Draft{
		EventID: eventID,
		Actor:   audit.ActorSystem,
		Type:    audit.EventToolResult,
		Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func appendNote(t *testing.T, store *audit.Store, runID domain.RunID) {
	t.Helper()
	if _, err := store.Append(context.Background(), runID, audit.Draft{
		EventID: "note",
		Actor:   audit.ActorSystem,
		Type:    audit.EventNote,
		Payload: map[string]any{"message": "no evidence"},
	}); err != nil {
		t.Fatal(err)
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNewCompilerRequiresJournal(t *testing.T) {
	_, err := NewCompiler(nil)
	if err == nil {
		t.Fatal("NewCompiler accepted nil journal")
	}
	if errors.Is(err, audit.ErrRunNotFound) {
		t.Fatalf("unexpected wrapped error: %v", err)
	}
}
