package middleware

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/message"
)

type debouncedService struct {
	message.Service
	interval time.Duration

	mu      sync.Mutex
	entries map[string]*debounceEntry
}

type debounceEntry struct {
	mu     sync.Mutex
	timer  *time.Timer
	latest message.Message
}

// WithDebounce wraps Update for assistant messages: lead edge immediate, trailing flush once per interval.
func WithDebounce(interval time.Duration) Middleware {
	return func(s message.Service) message.Service {
		return &debouncedService{Service: s, interval: interval, entries: make(map[string]*debounceEntry)}
	}
}

func (d *debouncedService) getOrCreate(id string) *debounceEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.entries[id]; ok {
		return e
	}
	e := &debounceEntry{}
	d.entries[id] = e
	return e
}

func (d *debouncedService) deleteEntry(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, id)
}

// Writer overrides (Update/Delete); others forwarded via embedded Service
func (d *debouncedService) Update(ctx context.Context, msg message.Message) error {
	// For non-assistant or finished messages, bypass debounce entirely.
	if msg.Role != message.Assistant || msg.IsFinished() {
		// Ensure any pending entry is cancelled on final assistant update
		if msg.Role == message.Assistant {
			if e := d.getOrCreate(msg.ID); e != nil {
				e.mu.Lock()
				e.latest = msg
				if e.timer != nil {
					e.timer.Stop()
					e.timer = nil
				}
				e.mu.Unlock()
				d.deleteEntry(msg.ID)
			}
		}
		return d.Service.Update(ctx, msg)
	}

	e := d.getOrCreate(msg.ID)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.latest = msg

	if e.timer == nil {
		// Leading edge: send immediately
		if err := d.Service.Update(ctx, msg); err != nil {
			return err
		}
		// Trailing flush after interval
		e.timer = time.AfterFunc(d.interval, func() {
			e.mu.Lock()
			latest := e.latest
			e.timer = nil
			e.mu.Unlock()
			if err := d.Service.Update(context.Background(), latest); err != nil {
				slog.Error("debounce.flush.update_failed", "message_id", latest.ID, "error", err)
			}
		})
	}
	return nil
}

func (d *debouncedService) Delete(ctx context.Context, id string) error {
	// Best-effort: cancel any pending timer for this id
	if e := d.getOrCreate(id); e != nil {
		e.mu.Lock()
		if e.timer != nil {
			e.timer.Stop()
			e.timer = nil
		}
		e.mu.Unlock()
		d.deleteEntry(id)
	}
	return d.Service.Delete(ctx, id)
}
