package guardian

import (
	"testing"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
	"github.com/Duang777/waybill-guardian/internal/tools"
)

func TestDamageClaimWaitsForApprovalAndExecutesExactlyOnce(t *testing.T) {
	loaded, err := filestore.Load("../../data/simulated/waybills-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := tools.NewFixtureWriteRuntime(loaded.Reads)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{
		DataDir:      t.TempDir(),
		Reads:        loaded.Reads,
		WriteRuntime: runtime,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartRun(t.Context(), "YD2026100007")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, service, run.RunID)
	if len(pending.Items) != 4 {
		t.Fatalf("approval items = %d, want 4", len(pending.Items))
	}
	if runtime.WriteCount(domain.ActionCreateClaim) != 0 {
		t.Fatal("claim executed before approval")
	}

	decided, err := service.Decide(t.Context(), pending.ID, DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "claim-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decided.Status != approval.StatusExecuted {
		t.Fatalf("approval status = %q, want executed", decided.Status)
	}
	if runtime.WriteCount(domain.ActionCreateClaim) != 1 {
		t.Fatalf(
			"claim writes = %d, want 1",
			runtime.WriteCount(domain.ActionCreateClaim),
		)
	}

	if _, err := service.Decide(t.Context(), pending.ID, DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "claim-test",
	}); err != nil {
		t.Fatal(err)
	}
	if runtime.WriteCount(domain.ActionCreateClaim) != 1 {
		t.Fatal("repeated confirmation executed the claim again")
	}
}
