package guardian

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"sync"

	agentkit "github.com/Duang777/waybill-guardian/internal/agent"
	"github.com/Duang777/waybill-guardian/internal/domain"
)

const (
	briefContractVersion = "overview-brief-v2"
	maxBriefCacheEntries = 128
)

type briefCacheKey [sha256.Size]byte

type briefCacheOutcome struct {
	Brief   ExecutiveBrief
	Failure BriefFallbackReason
}

type briefCacheEntry struct {
	ready    chan struct{}
	complete bool
	outcome  briefCacheOutcome
}

type briefCache struct {
	entries map[briefCacheKey]*briefCacheEntry
	mu      sync.Mutex
}

func newBriefCache() *briefCache {
	return &briefCache{
		entries: make(map[briefCacheKey]*briefCacheEntry),
	}
}

func newBriefCacheKey(
	waybillIDs []domain.WaybillID,
	asOf string,
	input agentkit.BriefInput,
) (briefCacheKey, error) {
	scope := make([]string, 0, len(waybillIDs))
	for _, id := range waybillIDs {
		scope = append(scope, string(id))
	}
	sort.Strings(scope)
	if len(scope) > 1 {
		compacted := scope[:1]
		for _, id := range scope[1:] {
			if id != compacted[len(compacted)-1] {
				compacted = append(compacted, id)
			}
		}
		scope = compacted
	}
	payload, err := json.Marshal(struct {
		Contract string              `json:"contract"`
		Scope    []string            `json:"scope"`
		AsOf     string              `json:"as_of"`
		Input    agentkit.BriefInput `json:"input"`
	}{
		Contract: briefContractVersion,
		Scope:    scope,
		AsOf:     asOf,
		Input:    input,
	})
	if err != nil {
		return briefCacheKey{}, err
	}
	return sha256.Sum256(payload), nil
}

func (c *briefCache) getOrLoad(
	ctx context.Context,
	key briefCacheKey,
	load func() briefCacheOutcome,
) (briefCacheOutcome, error) {
	for {
		c.mu.Lock()
		entry := c.entries[key]
		if entry == nil {
			c.evictCompletedLocked()
			entry = &briefCacheEntry{ready: make(chan struct{})}
			c.entries[key] = entry
			c.mu.Unlock()

			outcome := cloneBriefCacheOutcome(load())

			c.mu.Lock()
			entry.outcome = outcome
			entry.complete = true
			close(entry.ready)
			c.mu.Unlock()
			return cloneBriefCacheOutcome(outcome), nil
		}
		if entry.complete {
			outcome := cloneBriefCacheOutcome(entry.outcome)
			c.mu.Unlock()
			return outcome, nil
		}
		ready := entry.ready
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			return briefCacheOutcome{}, ctx.Err()
		case <-ready:
		}
	}
}

func (c *briefCache) evictCompletedLocked() {
	if len(c.entries) < maxBriefCacheEntries {
		return
	}
	for key, entry := range c.entries {
		if entry.complete {
			delete(c.entries, key)
			return
		}
	}
}

func cloneBriefCacheOutcome(value briefCacheOutcome) briefCacheOutcome {
	value.Brief = cloneExecutiveBrief(value.Brief)
	return value
}

func cloneExecutiveBrief(value ExecutiveBrief) ExecutiveBrief {
	value.Items = append([]ExecutiveBriefItem(nil), value.Items...)
	for index := range value.Items {
		value.Items[index].Evidence = append(
			[]EvidenceCitation(nil),
			value.Items[index].Evidence...,
		)
	}
	return value
}
