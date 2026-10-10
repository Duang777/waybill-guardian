package validate

import (
	"encoding/json"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func FuzzValidatorNeverPanicsOnDecodedContracts(f *testing.F) {
	problem, plan, at := validCase(f)
	problemJSON, err := json.Marshal(problem)
	if err != nil {
		f.Fatal(err)
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(problemJSON, planJSON)
	f.Add([]byte(`{}`), []byte(`{}`))

	validator := New(domain.ValidatorIdentity{
		Name: "independent", Version: "1.0.0", Build: "fuzz",
	})
	f.Fuzz(func(t *testing.T, rawProblem, rawPlan []byte) {
		var candidateProblem domain.ProblemSnapshot
		if err := json.Unmarshal(rawProblem, &candidateProblem); err != nil {
			return
		}
		var candidatePlan domain.Plan
		if err := json.Unmarshal(rawPlan, &candidatePlan); err != nil {
			return
		}
		_ = validator.Validate(candidateProblem, candidatePlan, at)
	})
}
