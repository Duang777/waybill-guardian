package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/jackc/pgx/v5"
)

type deliveryStreamKey struct {
	tenantID      deliverydomain.TenantID
	aggregateType deliveryservice.AggregateType
	aggregateID   string
}

type deliverySubscription struct {
	key    deliveryStreamKey
	events chan deliveryservice.Event
	wake   chan struct{}
	cancel context.CancelFunc
	close  func()
	once   sync.Once
}

func (subscription *deliverySubscription) Events() <-chan deliveryservice.Event {
	return subscription.events
}

func (subscription *deliverySubscription) Close() {
	if subscription == nil {
		return
	}
	subscription.once.Do(func() {
		subscription.cancel()
		subscription.close()
	})
}

func (store *DeliveryStore) Replay(
	ctx context.Context,
	cursor deliveryservice.StreamCursor,
) ([]deliveryservice.Event, error) {
	var lastSeq uint64
	if err := store.db.pool.QueryRow(ctx, `
		SELECT last_seq
		FROM waybill.delivery_event_heads
		WHERE tenant_id = $1
		  AND aggregate_type = $2
		  AND aggregate_id = $3
	`, cursor.TenantID, cursor.AggregateType,
		cursor.AggregateID).Scan(&lastSeq); errors.Is(err, pgx.ErrNoRows) {
		return nil, deliveryservice.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if cursor.After > lastSeq {
		return nil, deliveryservice.ErrCursorAhead
	}
	return store.replayDeliveryUnchecked(ctx, cursor)
}

func (store *DeliveryStore) replayDeliveryUnchecked(
	ctx context.Context,
	cursor deliveryservice.StreamCursor,
) ([]deliveryservice.Event, error) {
	expectedPrev := deliveryGenesisHash
	if cursor.After > 0 {
		if err := store.db.pool.QueryRow(ctx, `
			SELECT hash
			FROM waybill.delivery_events
			WHERE tenant_id = $1
			  AND aggregate_type = $2
			  AND aggregate_id = $3
			  AND seq = $4
		`, cursor.TenantID, cursor.AggregateType, cursor.AggregateID,
			cursor.After).Scan(&expectedPrev); errors.Is(err, pgx.ErrNoRows) {
			return nil, deliveryservice.ErrCursorAhead
		} else if err != nil {
			return nil, err
		}
	}
	rows, err := store.db.pool.Query(ctx, `
		SELECT schema_version,
		       tenant_id,
		       aggregate_type,
		       aggregate_id,
		       seq,
		       event_id,
		       event_type,
		       actor,
		       occurred_at,
		       payload_canonical,
		       prev_hash,
		       hash
		FROM (
			SELECT 1 AS schema_version,
			       tenant_id,
			       aggregate_type,
			       aggregate_id,
			       seq,
			       event_id,
			       event_type,
			       actor,
			       occurred_at,
			       payload_canonical,
			       prev_hash,
			       hash
			FROM waybill.delivery_events
			WHERE tenant_id = $1
			  AND aggregate_type = $2
			  AND aggregate_id = $3
			  AND seq > $4
		) event_rows
		ORDER BY seq
	`, cursor.TenantID, cursor.AggregateType, cursor.AggregateID, cursor.After)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []deliveryservice.Event
	for rows.Next() {
		var event deliveryservice.Event
		if err := rows.Scan(
			&event.SchemaVersion,
			&event.TenantID,
			&event.AggregateType,
			&event.AggregateID,
			&event.Seq,
			&event.EventID,
			&event.Type,
			&event.Actor,
			&event.OccurredAt,
			&event.Payload,
			&event.PrevHash,
			&event.Hash,
		); err != nil {
			return nil, err
		}
		if event.PrevHash != expectedPrev {
			return nil, fmt.Errorf("delivery event hash chain is broken at seq %d", event.Seq)
		}
		hash, err := hashDeliveryEvent(event)
		if err != nil || hash != event.Hash {
			return nil, fmt.Errorf("delivery event hash is invalid at seq %d", event.Seq)
		}
		expectedPrev = event.Hash
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func (store *DeliveryStore) Subscribe(
	ctx context.Context,
	cursor deliveryservice.StreamCursor,
) (deliveryservice.Subscription, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	subscription := &deliverySubscription{
		key: deliveryStreamKey{
			tenantID:      cursor.TenantID,
			aggregateType: cursor.AggregateType,
			aggregateID:   cursor.AggregateID,
		},
		wake:   make(chan struct{}, 1),
		cancel: cancel,
	}
	store.mu.Lock()
	id := store.nextSubID
	store.nextSubID++
	store.subscriptions[id] = subscription
	store.mu.Unlock()
	unregister := func() {
		store.mu.Lock()
		if current, exists := store.subscriptions[id]; exists && current == subscription {
			delete(store.subscriptions, id)
		}
		store.mu.Unlock()
	}
	subscription.close = unregister

	backlog, err := store.Replay(streamCtx, cursor)
	if err != nil {
		subscription.Close()
		return nil, err
	}
	size := len(backlog) + 64
	if size < 64 {
		size = 64
	}
	subscription.events = make(chan deliveryservice.Event, size)
	current := cursor.After
	for _, event := range backlog {
		subscription.events <- event
		current = event.Seq
	}
	go store.pollDeliverySubscription(streamCtx, subscription, current)
	return subscription, nil
}

func (store *DeliveryStore) pollDeliverySubscription(
	ctx context.Context,
	subscription *deliverySubscription,
	after uint64,
) {
	defer close(subscription.events)
	defer subscription.close()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-subscription.wake:
		case <-ticker.C:
		}
		events, err := store.replayDeliveryUnchecked(ctx, deliveryservice.StreamCursor{
			TenantID:      subscription.key.tenantID,
			AggregateType: subscription.key.aggregateType,
			AggregateID:   subscription.key.aggregateID,
			After:         after,
		})
		if err != nil {
			return
		}
		for _, event := range events {
			select {
			case <-ctx.Done():
				return
			case subscription.events <- event:
				after = event.Seq
			}
		}
	}
}
