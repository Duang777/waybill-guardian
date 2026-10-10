package solve

import (
	"context"
	"strconv"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type searchMove interface {
	Operator() OperatorID
	Key() string
	Apply(context.Context, *engine, candidateState) (domain.Plan, error)
}

type operatorFunc struct {
	operatorID OperatorID
	enumerate  func(context.Context, *engine, candidateState, func(searchMove) bool) error
}

func (operator operatorFunc) ID() OperatorID {
	return operator.operatorID
}

func (operator operatorFunc) Enumerate(
	ctx context.Context,
	engine *engine,
	state candidateState,
	yield func(searchMove) bool,
) error {
	return operator.enumerate(ctx, engine, state, yield)
}

func moveKey(operator OperatorID, values ...int) string {
	var result strings.Builder
	result.WriteString(string(operator))
	for _, value := range values {
		result.WriteByte('/')
		text := strconv.Itoa(value)
		for padding := 6 - len(text); padding > 0; padding-- {
			result.WriteByte('0')
		}
		result.WriteString(text)
	}
	return result.String()
}
