package domain

import "strings"

func ComputeProblemDigest(problem ProblemSnapshot) (ArtifactDigest, error) {
	problem.ProblemDigest = ""
	return Digest(problem)
}

func ComputePolicyDigest(policy PlanningPolicy) (ArtifactDigest, error) {
	return Digest(policy)
}

func ComputeCommitmentDigest(commitments CommitmentSet) (ArtifactDigest, error) {
	return Digest(commitments)
}

func ComputePlanDigest(plan Plan) (ArtifactDigest, error) {
	plan.PlanID = ""
	plan.RevisionID = ""
	plan.PlanDigest = ""
	return Digest(plan)
}

func ComputeReportDigest(report ValidationReport) (ArtifactDigest, error) {
	report.ReportDigest = ""
	report.CreatedAt = report.CreatedAt.UTC()
	return Digest(report)
}

func ValidArtifactDigest(value ArtifactDigest) bool {
	const sha256HexLength = 64
	if len(value) != sha256HexLength || strings.ToLower(string(value)) != string(value) {
		return false
	}
	for _, current := range value {
		if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}
