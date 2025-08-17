package agent

import (
	"context"
	"log/slog"
	"sync"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// sessionSerializedMessageService serializes write operations (Create/Update/Delete)
// per session to guarantee in-order persistence across all assistant updates
// and tool result writes while allowing tools to execute in parallel.
// Reads (Get/List/Subscribe) bypass serialization.
//
// This avoids races where a debounced trailing flush (older state) could
// overwrite a newer structural update (e.g., tool_calls added).
//
// The queue is best-effort; it does not retry on DB errors.

type sessionSerializedMessageService struct {
	base message.Service

	mu      sync.Mutex
	workers map[string]*sessionWorker
}

type opKind int

const (
	opUpdate opKind = iota
	opCreate
	opDelete
	opDeleteSession
)

type op struct {
	kind       opKind
	sessionID  string
	msg        message.Message
	createSess string
	createReq  message.CreateMessageParams
	deleteID   string
	resCh      chan any
	errCh      chan error
}

type sessionWorker struct {
	id    string
	base  message.Service
	ch    chan op
	close chan struct{}
}

func newSessionWorker(id string, base message.Service) *sessionWorker {
	w := &sessionWorker{
		id:    id,
		base:  base,
		ch:    make(chan op, 256),
		close: make(chan struct{}),
	}
	go w.run()
	return w
}

func (w *sessionWorker) run() {
	for {
		select {
		case o := <-w.ch:
			switch o.kind {
			case opUpdate:
				o.errCh <- w.base.Update(context.Background(), o.msg)
			case opCreate:
				msg, err := w.base.Create(context.Background(), o.createSess, o.createReq)
				if err != nil {
					o.errCh <- err
					break
				}
				o.resCh <- msg
				o.errCh <- nil
			case opDelete:
				o.errCh <- w.base.Delete(context.Background(), o.deleteID)
			case opDeleteSession:
				o.errCh <- w.base.DeleteSessionMessages(context.Background(), o.sessionID)
			}
		case <-w.close:
			return
		}
	}
}

func NewSessionSerializedMessageService(base message.Service) message.Service {
	return &sessionSerializedMessageService{base: base, workers: make(map[string]*sessionWorker)}
}

func (s *sessionSerializedMessageService) worker(sessionID string) *sessionWorker {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.workers[sessionID]
	if ok {
		return w
	}
	w = newSessionWorker(sessionID, s.base)
	s.workers[sessionID] = w
	return w
}

// message.Service implementation

func (s *sessionSerializedMessageService) Subscribe(ctx context.Context) <-chan pubsub.Event[message.Message] { // forwarded via base
	return s.base.Subscribe(ctx)
}

func (s *sessionSerializedMessageService) Create(ctx context.Context, sessionID string, params message.CreateMessageParams) (message.Message, error) {
	w := s.worker(sessionID)
	resCh := make(chan any, 1)
	errCh := make(chan error, 1)
	select {
	case w.ch <- op{kind: opCreate, createSess: sessionID, createReq: params, resCh: resCh, errCh: errCh}:
		// ok
	case <-ctx.Done():
		return message.Message{}, ctx.Err()
	}
	select {
	case err := <-errCh:
		if err != nil {
			return message.Message{}, err
		}
		msg, _ := (<-resCh).(message.Message)
		return msg, nil
	case <-ctx.Done():
		return message.Message{}, ctx.Err()
	}
}

func (s *sessionSerializedMessageService) Update(ctx context.Context, msg message.Message) error {
	if msg.SessionID == "" {
		// Fall back to direct update; should not happen
		slog.Warn("session-serializer: Update without sessionID; bypassing queue", "message_id", msg.ID)
		return s.base.Update(ctx, msg)
	}
	w := s.worker(msg.SessionID)
	errCh := make(chan error, 1)
	select {
	case w.ch <- op{kind: opUpdate, sessionID: msg.SessionID, msg: msg, errCh: errCh}:
		// ok
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *sessionSerializedMessageService) Get(ctx context.Context, id string) (message.Message, error) {
	return s.base.Get(ctx, id)
}

func (s *sessionSerializedMessageService) List(ctx context.Context, sessionID string) ([]message.Message, error) {
	return s.base.List(ctx, sessionID)
}

func (s *sessionSerializedMessageService) Delete(ctx context.Context, id string) error {
	// Need sessionID; fetch first (read path is fine)
	msg, err := s.base.Get(ctx, id)
	if err != nil {
		return err
	}
	if msg.SessionID == "" {
		return s.base.Delete(ctx, id)
	}
	w := s.worker(msg.SessionID)
	errCh := make(chan error, 1)
	select {
	case w.ch <- op{kind: opDelete, sessionID: msg.SessionID, deleteID: id, errCh: errCh}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *sessionSerializedMessageService) DeleteSessionMessages(ctx context.Context, sessionID string) error {
	w := s.worker(sessionID)
	errCh := make(chan error, 1)
	select {
	case w.ch <- op{kind: opDeleteSession, sessionID: sessionID, errCh: errCh}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
