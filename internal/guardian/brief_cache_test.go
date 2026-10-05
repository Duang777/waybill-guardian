package guardian

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestBriefCacheDoesNotStartUntrackedLoadAtCapacity(t *testing.T) {
	cache := newBriefCache(t.Context(), time.Now)
	for index := range maxBriefCacheEntries {
		var key briefCacheKey
		key[0] = byte(index)
		cache.entries[key] = &briefCacheEntry{
			ready:   make(chan struct{}),
			waiters: 1,
		}
	}

	var calls atomic.Int32
	var key briefCacheKey
	key[0] = 0xff
	outcome, err := cache.getOrLoad(
		t.Context(),
		key,
		func(context.Context) briefCacheOutcome {
			calls.Add(1)
			return briefCacheOutcome{}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failure != BriefFallbackProvider {
		t.Fatalf("failure = %q, want provider fallback", outcome.Failure)
	}
	if calls.Load() != 0 {
		t.Fatalf("untracked loader calls = %d, want 0", calls.Load())
	}
	if len(cache.entries) != maxBriefCacheEntries {
		t.Fatalf("cache entries = %d, want %d", len(cache.entries), maxBriefCacheEntries)
	}
}

func TestBriefCacheKeepsCanceledLoadCapacityUntilLoaderExits(t *testing.T) {
	cache := newBriefCache(t.Context(), time.Now)
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		var key briefCacheKey
		_, err := cache.getOrLoad(
			ctx,
			key,
			func(context.Context) briefCacheOutcome {
				close(started)
				<-release
				return briefCacheOutcome{}
			},
		)
		done <- err
	}()

	<-started
	cancel()
	if err := <-done; err == nil {
		t.Fatal("canceled waiter returned no error")
	}
	cache.mu.Lock()
	entryCount := len(cache.entries)
	abandoned := false
	for _, entry := range cache.entries {
		abandoned = entry.abandoned
	}
	cache.mu.Unlock()
	if entryCount != 1 || !abandoned {
		t.Fatalf("canceled load capacity = %d, abandoned = %t", entryCount, abandoned)
	}

	close(release)
	cache.wait()
	cache.mu.Lock()
	entryCount = len(cache.entries)
	cache.mu.Unlock()
	if entryCount != 0 {
		t.Fatalf("completed abandoned entries = %d, want 0", entryCount)
	}
}
