package domain

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
