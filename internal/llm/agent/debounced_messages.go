package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// debouncedMessageService wraps a message.Service and throttles Update calls for
// streaming assistant messages to reduce pubsub/event bursts. It performs a
// leading-edge update immediately, then coalesces subsequent updates within the
// interval and flushes the latest state once per interval. Final updates (with
// Finish) and non-assistant/tool messages are forwarded immediately.
type debouncedMessageService struct {
	base     message.Service
	interval time.Duration

	mu      sync.Mutex
	entries map[string]*debounceEntry
}

type debounceEntry struct {
	mu     sync.Mutex
	timer  *time.Timer
	latest message.Message
}

func NewDebouncedMessageService(base message.Service, interval time.Duration) message.Service {
	return &debouncedMessageService{
		base:     base,
		interval: interval,
		entries:  make(map[string]*debounceEntry),
	}
}

// getOrCreate returns the entry for a message id.
func (d *debouncedMessageService) getOrCreate(id string) *debounceEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.entries[id]; ok {
		return e
	}
	e := &debounceEntry{}
	d.entries[id] = e
	return e
}

// deleteEntry removes the entry for a message id.
func (d *debouncedMessageService) deleteEntry(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, id)
}

func (d *debouncedMessageService) Subscribe(ctx context.Context) <-chan pubsub.Event[message.Message] {
	return d.base.Subscribe(ctx)
}

func (d *debouncedMessageService) Create(ctx context.Context, sessionID string, params message.CreateMessageParams) (message.Message, error) {
	return d.base.Create(ctx, sessionID, params)
}

func (d *debouncedMessageService) Update(ctx context.Context, msg message.Message) error {
	// For non-assistant messages or finished messages, bypass debounce.
	if msg.Role != message.Assistant || msg.IsFinished() {
		// On final update, ensure we cancel any pending timer and drop entry.
		if msg.Role == message.Assistant {
			if e := d.getOrCreate(msg.ID); e != nil {
				e.mu.Lock()
				// Set latest to final state so any in-flight flush applies the final message.
				e.latest = msg
				if e.timer != nil {
					e.timer.Stop()
					e.timer = nil
				}
				e.mu.Unlock()
				d.deleteEntry(msg.ID)
			}
		}
		return d.base.Update(ctx, msg)
	}

	e := d.getOrCreate(msg.ID)
	e.mu.Lock()
	defer e.mu.Unlock()
	// Always update latest to reflect the most recent state, including tool_calls.
	e.latest = msg

	// If there's no active timer, perform a leading-edge update now and schedule
	// the next flush. Subsequent calls within the interval will only update 'latest'.
	if e.timer == nil {
		if err := d.base.Update(ctx, msg); err != nil {
			return err
		}
		// Schedule trailing flush after interval to deliver any accumulated changes.
		e.timer = time.AfterFunc(d.interval, func() {
			e.mu.Lock()
			latest := e.latest
			e.timer = nil
			e.mu.Unlock()
			// Use background context to avoid coupling to streaming ctx cancellations.
			if err := d.base.Update(context.Background(), latest); err != nil {
				slog.Error("debounce.flush.update_failed", "message_id", latest.ID, "error", err)
			}
		})
	}
	return nil
}

func (d *debouncedMessageService) Get(ctx context.Context, id string) (message.Message, error) {
	return d.base.Get(ctx, id)
}

func (d *debouncedMessageService) List(ctx context.Context, sessionID string) ([]message.Message, error) {
	return d.base.List(ctx, sessionID)
}

func (d *debouncedMessageService) Delete(ctx context.Context, id string) error {
	// Best-effort: cancel any pending entry for this id.
	if e := d.getOrCreate(id); e != nil {
		e.mu.Lock()
		if e.timer != nil {
			e.timer.Stop()
			e.timer = nil
		}
		e.mu.Unlock()
		d.deleteEntry(id)
	}
	return d.base.Delete(ctx, id)
}

func (d *debouncedMessageService) DeleteSessionMessages(ctx context.Context, sessionID string) error {
	return d.base.DeleteSessionMessages(ctx, sessionID)
}
