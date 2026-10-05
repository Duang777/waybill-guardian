package guardian

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sort"
	"sync"
	"time"

	agentkit "github.com/Duang777/waybill-guardian/internal/agent"
	"github.com/Duang777/waybill-guardian/internal/domain"
)

const (
	briefContractVersion = "overview-brief-v2"
	maxBriefCacheEntries = 128
	briefFailureCacheTTL = time.Minute
)

type briefCacheKey [sha256.Size]byte

type briefCacheOutcome struct {
	Brief   ExecutiveBrief
	Failure BriefFallbackReason
}

type briefCacheEntry struct {
	ready     chan struct{}
	cancel    context.CancelFunc
	waiters   int
	complete  bool
	abandoned bool
	outcome   briefCacheOutcome
	expiresAt time.Time
}

type briefCache struct {
	entries map[briefCacheKey]*briefCacheEntry
	ctx     context.Context
	clock   func() time.Time
	mu      sync.Mutex
	wg      sync.WaitGroup
}

func newBriefCache(ctx context.Context, clock func() time.Time) *briefCache {
	if ctx == nil {
		ctx = context.Background()
	}
	if clock == nil {
		clock = time.Now
	}
	return &briefCache{
		entries: make(map[briefCacheKey]*briefCacheEntry),
		ctx:     ctx,
		clock:   clock,
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
	load func(context.Context) briefCacheOutcome,
) (briefCacheOutcome, error) {
	for {
		if err := ctx.Err(); err != nil {
			return briefCacheOutcome{}, err
		}
		c.mu.Lock()
		entry := c.entries[key]
		if entry != nil && entry.complete && c.entryExpiredLocked(entry) {
			delete(c.entries, key)
			entry = nil
		}
		if entry == nil {
			if !c.makeRoomLocked() {
				c.mu.Unlock()
				return briefCacheOutcome{Failure: BriefFallbackProvider}, nil
			}
			loadCtx, cancel := context.WithCancel(c.ctx)
			entry = &briefCacheEntry{
				ready:   make(chan struct{}),
				cancel:  cancel,
				waiters: 1,
			}
			c.entries[key] = entry
			c.wg.Add(1)
			go c.load(key, entry, loadCtx, load)
		} else if entry.complete {
			outcome := cloneBriefCacheOutcome(entry.outcome)
			c.mu.Unlock()
			return outcome, nil
		} else if entry.abandoned {
			c.mu.Unlock()
			return briefCacheOutcome{Failure: BriefFallbackProvider}, nil
		} else {
			entry.waiters++
		}
		ready := entry.ready
		c.mu.Unlock()

		select {
		case <-ctx.Done():
			c.releaseWaiter(entry)
			return briefCacheOutcome{}, ctx.Err()
		case <-ready:
			c.mu.Lock()
			if entry.waiters > 0 {
				entry.waiters--
			}
			outcome := cloneBriefCacheOutcome(entry.outcome)
			c.mu.Unlock()
			return outcome, nil
		}
	}
}

func (c *briefCache) load(
	key briefCacheKey,
	entry *briefCacheEntry,
	ctx context.Context,
	load func(context.Context) briefCacheOutcome,
) {
	defer c.wg.Done()
	outcome := cloneBriefCacheOutcome(load(ctx))

	c.mu.Lock()
	entry.outcome = outcome
	entry.complete = true
	if outcome.Failure != "" {
		entry.expiresAt = c.clock().Add(briefFailureCacheTTL)
	}
	entry.cancel()
	close(entry.ready)
	if entry.abandoned && c.entries[key] == entry {
		delete(c.entries, key)
	}
	if c.entries[key] != entry {
		entry.waiters = 0
	}
	c.mu.Unlock()
}

func (c *briefCache) releaseWaiter(entry *briefCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry.waiters > 0 {
		entry.waiters--
	}
	if entry.waiters == 0 && !entry.complete {
		entry.abandoned = true
		entry.cancel()
	}
}

func (c *briefCache) entryExpiredLocked(entry *briefCacheEntry) bool {
	return !entry.expiresAt.IsZero() && !c.clock().Before(entry.expiresAt)
}

func (c *briefCache) makeRoomLocked() bool {
	for len(c.entries) >= maxBriefCacheEntries {
		removed := false
		for key, entry := range c.entries {
			if entry.complete {
				delete(c.entries, key)
				removed = true
				break
			}
		}
		if !removed {
			return false
		}
	}
	return true
}

func (c *briefCache) wait() {
	c.wg.Wait()
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
