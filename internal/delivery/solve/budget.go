package solve

type evaluationCounter struct {
	limit    EvaluationBudget
	consumed EvaluationBudget
}

func newEvaluationCounter(limit EvaluationBudget) evaluationCounter {
	return evaluationCounter{limit: limit}
}

func (counter *evaluationCounter) Consume() (EvaluationBudget, bool) {
	if counter.consumed >= counter.limit {
		return counter.consumed, false
	}
	counter.consumed++
	return counter.consumed, true
}

func (counter evaluationCounter) Limit() EvaluationBudget {
	return counter.limit
}

func (counter evaluationCounter) Consumed() EvaluationBudget {
	return counter.consumed
}

func (counter evaluationCounter) Exhausted() bool {
	return counter.consumed >= counter.limit
}
