package e2e

func sseResponseCreated() SSE {
	return SSE{Data: map[string]any{
		"type": "response.created",
		"payload": map[string]any{
			"response": map[string]any{
				"id":         "resp_mock",
				"created_at": 0,
				"object":     "response",
				"model":      "gpt-4o-mini",
				"status":     "in_progress",
				"output":     []any{},
			},
		},
	}}
}

func sseOutputItemAdded(itemID string) SSE {
	return SSE{Data: map[string]any{
		"type": "response.output_item.added",
		"payload": map[string]any{
			"item": map[string]any{
				"id":     itemID,
				"type":   "message",
				"role":   "assistant",
				"status": "in_progress",
				"content": []any{},
			},
			"output_index": 0,
		},
	}}
}

func sseContentPartAdded(itemID string) SSE {
	return SSE{Data: map[string]any{
		"type": "response.content_part.added",
		"payload": map[string]any{
			"item_id":       itemID,
			"content_index": 0,
			"output_index":  0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
				"logprobs":    []any{},
			},
		},
	}}
}

func sseTextDelta(text string, itemID string) SSE {
	return SSE{Data: map[string]any{
		"type": "response.output_text.delta",
		"payload": map[string]any{
			"delta":         text,
			"item_id":       itemID,
			"content_index": 0,
			"output_index":  0,
		},
	}}
}

func sseTextDone() SSE {
	return SSE{Data: map[string]any{
		"type":    "response.output_text.done",
		"payload": map[string]any{"done": true},
	}}
}

func sseContentPartDone(itemID, text string) SSE {
	return SSE{Data: map[string]any{
		"type": "response.content_part.done",
		"payload": map[string]any{
			"item_id":       itemID,
			"content_index": 0,
			"output_index":  0,
			"part": map[string]any{
				"type": "output_text",
				"text": text,
			},
		},
	}}
}

func sseOutputItemDone(itemID, text string) SSE {
	return SSE{Data: map[string]any{
		"type": "response.output_item.done",
		"payload": map[string]any{
			"item": map[string]any{
				"id":     itemID,
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []any{
					map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}},
				},
			},
			"output_index": 0,
		},
	}}
}

func sseCompletedText(text string, itemID string) SSE {
	return SSE{Data: map[string]any{
		"type": "response.completed",
		"payload": map[string]any{
			"response": map[string]any{
				"id":                 "resp_mock",
				"created_at":         0,
				"object":             "response",
				"model":              "gpt-4o-mini",
				"status":             "completed",
				"error":              map[string]any{"code": "", "message": ""},
				"incomplete_details": map[string]any{"reason": ""},
				"parallel_tool_calls": true,
				"temperature":         1,
				"top_p":               1,
				"tool_choice":         map[string]any{"OfToolChoiceMode": "auto", "type": "", "name": "", "server_label": ""},
				"text":                map[string]any{"format": map[string]any{"type": "text", "name": "", "schema": nil, "description": "", "strict": false}},
				"usage": map[string]any{
					"input_tokens":  0,
					"output_tokens": 0,
					"total_tokens":  0,
					"input_tokens_details":  map[string]any{"cached_tokens": 0},
					"output_tokens_details": map[string]any{"reasoning_tokens": 0},
				},
				"output": []any{
					map[string]any{
						"id":     itemID,
						"type":   "message",
						"role":   "assistant",
						"status": "completed",
						"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}},
					},
				},
			},
		},
	}}
}

func actionEmit(events ...SSE) Action { return Action{Emit: events} }
func actionClose() Action { return Action{Close: true} }
