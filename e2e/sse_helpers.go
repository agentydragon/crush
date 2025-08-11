package e2e

func sseOutputItemAdded(itemID string) SSE {
	return SSE{Data: map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{
			"id":     itemID,
			"type":   "message",
			"role":   "assistant",
			"status": "in_progress",
			"content": []any{},
		},
	}}
}

func sseContentPartAdded(itemID string) SSE {
	return SSE{Data: map[string]any{
		"type":          "response.content_part.added",
		"item_id":       itemID,
		"content_index": 0,
		"output_index":  0,
		"part": map[string]any{
			"type":       "output_text",
			"text":       "",
			"annotations": []any{},
			"logprobs":   []any{},
		},
	}}
}

func sseTextDelta(text string, itemID string) SSE {
	return SSE{Data: map[string]any{
		"type":          "response.output_text.delta",
		"delta":         text,
		"item_id":       itemID,
		"content_index": 0,
		"output_index":  0,
	}}
}

func sseTextDone() SSE {
	return SSE{Data: map[string]any{"type": "response.output_text.done", "done": true}}
}

func sseContentPartDone(itemID, text string) SSE {
	return SSE{Data: map[string]any{
		"type":          "response.content_part.done",
		"item_id":       itemID,
		"content_index": 0,
		"output_index":  0,
		"part": map[string]any{
			"type": "output_text",
			"text": text,
		},
	}}
}

func sseOutputItemDone(itemID, text string) SSE {
	return SSE{Data: map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{
			"id":     itemID,
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []any{
				map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}},
			},
		},
	}}
}

func sseCompletedText(text string, itemID string) SSE {
	return SSE{Data: map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
			"output": []any{
				map[string]any{
					"id":     itemID,
					"type":   "message",
					"role":   "assistant",
					"status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": text}},
				},
			},
		},
	}}
}

func actionEmit(events ...SSE) Action { return Action{Emit: events} }
func actionClose() Action { return Action{Close: true} }
