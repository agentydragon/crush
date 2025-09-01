package message

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/google/uuid"
)

type CreateMessageParams struct {
	Role     MessageRole
	Parts    []ContentPart
	Model    string
	Provider string
}

type Service interface {
	pubsub.Suscriber[Message]
	Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error)
	Update(ctx context.Context, message Message) error
	Get(ctx context.Context, id string) (Message, error)
	List(ctx context.Context, sessionID string) ([]Message, error)
	// ListChanges returns session-scoped changes since the provided watermarks (seconds + id tie-breaker).
	ListChanges(ctx context.Context, sessionID string, wm Watermarks, limit int) (Changes, Watermarks, error)
	Delete(ctx context.Context, id string) error
	DeleteSessionMessages(ctx context.Context, sessionID string) error
}

type service struct {
	*pubsub.Broker[Message]
	q db.Querier
}

// Watermarks carry per-stream positions for delta polling (seconds + id tie-breaker)
type Watermarks struct {
	MessagesTS int64  // updated_at seconds for messages
	MessagesID string // last message id at MessagesTS
	ToolTS     int64  // created_at seconds for Role=tool messages
	ToolID     string // last tool message id at ToolTS
}

type Changes struct {
	Messages []Message
	ToolMsgs []Message
}

func NewService(q db.Querier) Service {
	return &service{
		Broker: pubsub.NewBroker[Message](),
		q:      q,
	}
}

func (s *service) Delete(ctx context.Context, id string) error {
	message, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.q.DeleteMessage(ctx, message.ID)
	if err != nil {
		return err
	}
	s.Publish(pubsub.DeletedEvent, message)
	return nil
}

func (s *service) Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error) {
	slog.Info("message.Create: begin", "session_id", sessionID, "role", params.Role, "model", params.Model, "provider", params.Provider)
	if params.Role != Assistant {
		params.Parts = append(params.Parts, Finish{
			Reason: FinishReasonEndTurn,
		})
	}
	partsJSON, err := marshallParts(params.Parts)
	if err != nil {
		return Message{}, err
	}
	dbMessage, err := s.q.CreateMessage(ctx, db.CreateMessageParams{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Role:      string(params.Role),
		Parts:     string(partsJSON),
		Model:     sql.NullString{String: string(params.Model), Valid: true},
		Provider:  sql.NullString{String: params.Provider, Valid: params.Provider != ""},
	})
	if err != nil {
		return Message{}, err
	}
	message, err := s.fromDBItem(dbMessage)
	if err != nil {
		return Message{}, err
	}
	slog.Info("message.Create: saved", "message_id", message.ID, "role", message.Role, "text_len", len(message.Content().Text))
	s.Publish(pubsub.CreatedEvent, message)
	return message, nil
}

func (s *service) DeleteSessionMessages(ctx context.Context, sessionID string) error {
	messages, err := s.List(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if message.SessionID == sessionID {
			err = s.Delete(ctx, message.ID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// ListChanges fetches deltas for a session since provided watermarks using sqlc-generated queries
func (s *service) ListChanges(ctx context.Context, sessionID string, wm Watermarks, limit int) (Changes, Watermarks, error) {
	msgRows, err := s.q.ListSessionMessageChanges(ctx, db.ListSessionMessageChangesParams{
		SessionID: sessionID,
		UpdatedAt: wm.MessagesTS,
		ID:        wm.MessagesID,
		Limit:     int64(limit),
	})
	if err != nil {
		return Changes{}, wm, err
	}
	toolRows, err := s.q.ListSessionToolMessageChanges(ctx, db.ListSessionToolMessageChangesParams{
		SessionID: sessionID,
		CreatedAt: wm.ToolTS,
		ID:        wm.ToolID,
		Limit:     int64(limit),
	})
	if err != nil {
		return Changes{}, wm, err
	}
	ch := Changes{Messages: make([]Message, 0, len(msgRows)), ToolMsgs: make([]Message, 0, len(toolRows))}
	for _, r := range msgRows {
		m, err := s.fromDBItem(r)
		if err != nil {
			return Changes{}, wm, err
		}
		ch.Messages = append(ch.Messages, m)
		if r.UpdatedAt > wm.MessagesTS || (r.UpdatedAt == wm.MessagesTS && r.ID > wm.MessagesID) {
			wm.MessagesTS = r.UpdatedAt
			wm.MessagesID = r.ID
		}
	}
	for _, r := range toolRows {
		m, err := s.fromDBItem(r)
		if err != nil {
			return Changes{}, wm, err
		}
		ch.ToolMsgs = append(ch.ToolMsgs, m)
		if r.CreatedAt > wm.ToolTS || (r.CreatedAt == wm.ToolTS && r.ID > wm.ToolID) {
			wm.ToolTS = r.CreatedAt
			wm.ToolID = r.ID
		}
	}
	return ch, wm, nil
}

func (s *service) Update(ctx context.Context, message Message) error {
	slog.Info("message.Update: begin", "message_id", message.ID, "finished", message.IsFinished(), "text_len", len(message.Content().Text))
	// Ensure deterministic ordering of parts to avoid flakiness when several parts are added quickly.
	// Canonical order used across the codebase: text, reasoning(summary/encrypted), tool_call(s), tool_result(s), finish (last).
	partsStable := make([]ContentPart, len(message.Parts))
	copy(partsStable, message.Parts)
	sort.SliceStable(partsStable, func(i, j int) bool {
		prio := func(p ContentPart) int {
			switch p.(type) {
			case TextContent:
				return 1
			case ReasoningContent, ReasoningSummaryContent, ReasoningEncryptedContent:
				return 2
			case ToolCall:
				return 3
			case ToolResult:
				return 4
			case Finish:
				return 5
			default:
				return 100
			}
		}
		pi, pj := prio(partsStable[i]), prio(partsStable[j])
		if pi != pj {
			return pi < pj
		}
		return i < j
	})
	// Keep only the last Finish part if multiple exist
	finIdx := -1
	for i, p := range partsStable {
		if _, ok := p.(Finish); ok {
			finIdx = i
		}
	}
	if finIdx >= 0 {
		last := partsStable[finIdx]
		// remove all finishes and append last
		filtered := make([]ContentPart, 0, len(partsStable))
		for _, p := range partsStable {
			if _, ok := p.(Finish); ok {
				continue
			}
			filtered = append(filtered, p)
		}
		partsStable = append(filtered, last)
	}

	parts, err := marshallParts(partsStable)
	if err != nil {
		return err
	}
	finishedAt := sql.NullInt64{}
	if f := message.FinishPart(); f != nil {
		finishedAt.Int64 = f.Time
		finishedAt.Valid = true
	}
	err = s.q.UpdateMessage(ctx, db.UpdateMessageParams{
		ID:         message.ID,
		Parts:      string(parts),
		FinishedAt: finishedAt,
	})
	if err != nil {
		return err
	}
	message.UpdatedAt = time.Now().UnixMicro()
	slog.Info("message.Update: saved", "message_id", message.ID, "finished", message.IsFinished(), "text_len", len(message.Content().Text))
	s.Publish(pubsub.UpdatedEvent, message)
	return nil
}

func (s *service) Get(ctx context.Context, id string) (Message, error) {
	dbMessage, err := s.q.GetMessage(ctx, id)
	if err != nil {
		return Message{}, err
	}
	return s.fromDBItem(dbMessage)
}

func (s *service) List(ctx context.Context, sessionID string) ([]Message, error) {
	dbMessages, err := s.q.ListMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	// Deterministic order: created_at then ID. No role-based tie-breakers.
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].CreatedAt != messages[j].CreatedAt {
			return messages[i].CreatedAt < messages[j].CreatedAt
		}
		return messages[i].ID < messages[j].ID
	})
	return messages, nil
}

func (s *service) fromDBItem(item db.Message) (Message, error) {
	parts, err := unmarshallParts([]byte(item.Parts))
	if err != nil {
		return Message{}, err
	}
	return Message{
		ID:        item.ID,
		SessionID: item.SessionID,
		Role:      MessageRole(item.Role),
		Parts:     parts,
		Model:     item.Model.String,
		Provider:  item.Provider.String,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
	}, nil
}

type partType string

const (
	reasoningType          partType = "reasoning"
	reasoningEncryptedType partType = "reasoning_encrypted"
	textType               partType = "text"
	imageURLType           partType = "image_url"
	binaryType             partType = "binary"
	toolCallType           partType = "tool_call"
	toolResultType         partType = "tool_result"
	finishType             partType = "finish"
)

type partWrapper struct {
	Type partType    `json:"type"`
	Data ContentPart `json:"data"`
}

func marshallParts(parts []ContentPart) ([]byte, error) {
	wrappedParts := make([]partWrapper, len(parts))

	for i, part := range parts {
		var typ partType

		switch part.(type) {
		case ReasoningSummaryContent:
			typ = reasoningType
		case ReasoningEncryptedContent:
			typ = reasoningEncryptedType
		case TextContent:
			typ = textType
		case ImageURLContent:
			typ = imageURLType
		case BinaryContent:
			typ = binaryType
		case ToolCall:
			typ = toolCallType
		case ToolResult:
			typ = toolResultType
		case Finish:
			typ = finishType
		default:
			return nil, fmt.Errorf("unknown part type: %T", part)
		}

		wrappedParts[i] = partWrapper{
			Type: typ,
			Data: part,
		}
	}
	return json.Marshal(wrappedParts)
}

func unmarshallParts(data []byte) ([]ContentPart, error) {
	temp := []json.RawMessage{}

	if err := json.Unmarshal(data, &temp); err != nil {
		return nil, err
	}

	// Decoder registry: maps part types to specific unmarshal functions that
	// return concrete value-types (not pointers) to preserve existing type
	// assertions throughout the codebase (e.g., part.(TextContent)).
	decoders := map[partType]func([]byte) (ContentPart, error){
		reasoningType: func(b []byte) (ContentPart, error) {
			var p ReasoningSummaryContent
			if err := json.Unmarshal(b, &p); err != nil { return nil, err }
			return p, nil
		},
		reasoningEncryptedType: func(b []byte) (ContentPart, error) {
			var p ReasoningEncryptedContent
			if err := json.Unmarshal(b, &p); err != nil { return nil, err }
			return p, nil
		},
		textType: func(b []byte) (ContentPart, error) {
			var p TextContent
			if err := json.Unmarshal(b, &p); err != nil { return nil, err }
			return p, nil
		},
		imageURLType: func(b []byte) (ContentPart, error) {
			var p ImageURLContent
			if err := json.Unmarshal(b, &p); err != nil { return nil, err }
			return p, nil
		},
		binaryType: func(b []byte) (ContentPart, error) {
			var p BinaryContent
			if err := json.Unmarshal(b, &p); err != nil { return nil, err }
			return p, nil
		},
		toolCallType: func(b []byte) (ContentPart, error) {
			var p ToolCall
			if err := json.Unmarshal(b, &p); err != nil { return nil, err }
			return p, nil
		},
		toolResultType: func(b []byte) (ContentPart, error) {
			var p ToolResult
			if err := json.Unmarshal(b, &p); err != nil { return nil, err }
			return p, nil
		},
		finishType: func(b []byte) (ContentPart, error) {
			var p Finish
			if err := json.Unmarshal(b, &p); err != nil { return nil, err }
			return p, nil
		},
	}

	parts := make([]ContentPart, 0, len(temp))

	for _, rawPart := range temp {
		var wrapper struct {
			Type partType        `json:"type"`
			Data json.RawMessage `json:"data"`
		}

		if err := json.Unmarshal(rawPart, &wrapper); err != nil {
			return nil, err
		}

		decode, ok := decoders[wrapper.Type]
		if !ok {
			return nil, fmt.Errorf("unknown part type: %s", wrapper.Type)
		}
		p, err := decode(wrapper.Data)
		if err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}

	return parts, nil
}
