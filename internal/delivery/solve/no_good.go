package solve

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type noGoodKey struct {
	stateKey stateKey
	moveKey  string
}

type noGoodRecord struct {
	StateKey    domain.ArtifactDigest `json:"state_key"`
	MoveKey     string                `json:"move_key"`
	FailureCode failureCode           `json:"failure_code"`
}

type noGoodStore struct {
	problemDigest domain.ArtifactDigest
	configDigest  domain.ArtifactDigest
	solver        domain.SolverIdentity
	records       map[noGoodKey]failureCode
	learned       uint64
	applied       uint64
}

func newNoGoodStore(
	problemDigest domain.ArtifactDigest,
	configDigest domain.ArtifactDigest,
	solver domain.SolverIdentity,
) noGoodStore {
	return noGoodStore{
		problemDigest: problemDigest,
		configDigest:  configDigest,
		solver:        solver,
		records:       make(map[noGoodKey]failureCode),
	}
}

func (store *noGoodStore) Lookup(state stateKey, moveKey string) (failureCode, bool) {
	code, exists := store.records[noGoodKey{stateKey: state, moveKey: moveKey}]
	if exists {
		store.applied++
	}
	return code, exists
}

func (store *noGoodStore) Learn(
	state stateKey,
	moveKey string,
	code failureCode,
) {
	key := noGoodKey{stateKey: state, moveKey: moveKey}
	if _, exists := store.records[key]; exists {
		return
	}
	store.records[key] = code
	store.learned++
}

func (store noGoodStore) Digest() (domain.ArtifactDigest, error) {
	records := make([]noGoodRecord, 0, len(store.records))
	for key, code := range store.records {
		records = append(records, noGoodRecord{
			StateKey:    domain.ArtifactDigest(key.stateKey),
			MoveKey:     key.moveKey,
			FailureCode: code,
		})
	}
	slices.SortFunc(records, func(left, right noGoodRecord) int {
		if result := strings.Compare(
			string(left.StateKey),
			string(right.StateKey),
		); result != 0 {
			return result
		}
		if result := strings.Compare(left.MoveKey, right.MoveKey); result != 0 {
			return result
		}
		return strings.Compare(string(left.FailureCode), string(right.FailureCode))
	})
	digest, err := domain.Digest(struct {
		ProblemDigest domain.ArtifactDigest `json:"problem_digest"`
		ConfigDigest  domain.ArtifactDigest `json:"config_digest"`
		Solver        domain.SolverIdentity `json:"solver"`
		Records       []noGoodRecord        `json:"records"`
	}{
		ProblemDigest: store.problemDigest,
		ConfigDigest:  store.configDigest,
		Solver:        store.solver,
		Records:       records,
	})
	if err != nil {
		return "", fmt.Errorf("digest no-goods: %w", err)
	}
	return digest, nil
}
