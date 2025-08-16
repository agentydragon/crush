package pubsub

import (
	"context"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

var defaultBufferSize = 64

// global drop counters
var (
	dropsTotal atomic.Int64
	dropMu     sync.Mutex
	dropsByType    = map[string]int64{}
	dropsByTopic   = map[string]int64{}
	lastDropUnixMS atomic.Int64
	lastDropByTopic = map[string]int64{}
)

func SetDefaultBufferSize(n int) { if n > 0 { defaultBufferSize = n } }
func DropsTotal() int64 { return dropsTotal.Load() }
func DropsByType() map[string]int64 { dropMu.Lock(); defer dropMu.Unlock(); out := make(map[string]int64, len(dropsByType)); for k,v := range dropsByType { out[k]=v }; return out }
func LastDropUnixMS() int64 { return lastDropUnixMS.Load() }
func TopicDropsTotal(topic string) int64 { dropMu.Lock(); defer dropMu.Unlock(); return dropsByTopic[topic] }
func TopicLastDropUnixMS(topic string) int64 { dropMu.Lock(); defer dropMu.Unlock(); return lastDropByTopic[topic] }

// IncDrop allows external callers to record a drop in special paths (e.g., forward timeouts)
func IncDrop(topic string, typ string) {
	now := time.Now().UnixMilli()
	dropsTotal.Add(1)
	dropMu.Lock()
	dropsByType[typ]++
	dropsByTopic[topic]++
	lastDropByTopic[topic] = now
	dropMu.Unlock()
	lastDropUnixMS.Store(now)
	slog.Warn("pubsub.drop", "topic", topic, "type", typ)
}

type Broker[T any] struct {
	subs      map[chan Event[T]]struct{}
	mu        sync.RWMutex
	done      chan struct{}
	subCount  int
	maxEvents int
}

func NewBroker[T any]() *Broker[T] {
	return NewBrokerWithOptions[T](defaultBufferSize, 1000)
}

func NewBrokerWithOptions[T any](channelBufferSize, maxEvents int) *Broker[T] {
	b := &Broker[T]{
		subs:      make(map[chan Event[T]]struct{}),
		done:      make(chan struct{}),
		subCount:  0,
		maxEvents: maxEvents,
	}
	return b
}

func (b *Broker[T]) Shutdown() {
	select {
	case <-b.done: // Already closed
		return
	default:
		close(b.done)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for ch := range b.subs {
		delete(b.subs, ch)
		close(ch)
	}

	b.subCount = 0
}

func (b *Broker[T]) Subscribe(ctx context.Context) <-chan Event[T] {
	b.mu.Lock()
	defer b.mu.Unlock()

	select {
	case <-b.done:
		ch := make(chan Event[T])
		close(ch)
		return ch
	default:
	}

	sub := make(chan Event[T], defaultBufferSize)
	b.subs[sub] = struct{}{}
	b.subCount++

	go func() {
		<-ctx.Done()

		b.mu.Lock()
		defer b.mu.Unlock()

		select {
		case <-b.done:
			return
		default:
		}

		delete(b.subs, sub)
		close(sub)
		b.subCount--
	}()

	return sub
}

func (b *Broker[T]) GetSubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.subCount
}

func (b *Broker[T]) Publish(t EventType, payload T) {
	b.mu.RLock()
	select {
	case <-b.done:
		b.mu.RUnlock()
		return
	default:
	}

	subscribers := make([]chan Event[T], 0, len(b.subs))
	for sub := range b.subs {
		subscribers = append(subscribers, sub)
	}
	b.mu.RUnlock()

	event := Event[T]{Type: t, Payload: payload}

	for _, sub := range subscribers {
		select {
		case sub <- event:
		default:
			// Channel is full, subscriber is slow - skip this event and record a drop
			now := time.Now().UnixMilli()
			dropsTotal.Add(1)
			dropMu.Lock()
			dropsByType[string(t)]++
			topic := "unknown"
			// Best-effort derivation: for MCP events, look for Name field on payload
			if string(t) == string(UpdatedEvent) {
				v := reflect.ValueOf(payload)
				if v.Kind() == reflect.Struct {
					if f := v.FieldByName("Name"); f.IsValid() && f.Kind() == reflect.String {
						if s, ok := f.Interface().(string); ok && s != "" {
							topic = "mcp:" + s
						}
					}
				}
			}
			dropsByTopic[topic]++
			lastDropByTopic[topic] = now
			dropMu.Unlock()
			lastDropUnixMS.Store(now)
			slog.Warn("pubsub.drop", "type", string(t), "topic", topic)
		}
	}
}
