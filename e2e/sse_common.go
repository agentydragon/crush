package e2e

// Ergonomic, centralized SSE helpers used across scenarios/tests.
// These wrap the shapes used by mock_responses and keep the tests consistent.

// actionEmit groups SSE events into a single Action.
func actionEmit(es ...SSE) Action { return Action{Emit: es} }

// actionClose closes the SSE stream for the current step.
func actionClose() Action { return Action{Close: true} }

// Generic response.created event (first frame of a stream)
func sseResponseCreated() SSE {
	return SSE{Data: map[string]any{"type": "response.created", "response": map[string]any{"status": "in_progress"}}}
}

// Output message scaffolding (for text outputs, not function_call)
func sseOutputItemAdded(id string) SSE {
	if id == "" { panic("sseOutputItemAdded: id required") }
	return SSE{Data: map[string]any{"type": "response.output_item.added", "item": map[string]any{"type": "message", "id": id}}}
}
func sseContentPartAdded(itemID string) SSE {
	if itemID == "" { panic("sseContentPartAdded: itemID required") }
	return SSE{Data: map[string]any{"type": "response.output_message.content_part.added", "item_id": itemID, "content_index": 0, "part": map[string]any{"type": "output_text"}}}
}
func sseTextDelta(text, itemID string) SSE {
	if text == "" || itemID == "" { panic("sseTextDelta: text and itemID required") }
	return SSE{Data: map[string]any{"type": "response.output_text.delta", "item_id": itemID, "content_index": 0, "output_index": 0, "delta": text}}
}
func sseTextDone() SSE {
	return SSE{Data: map[string]any{"type": "response.output_text.done"}}
}
func sseContentPartDone(itemID, text string) SSE {
	if itemID == "" { panic("sseContentPartDone: itemID required") }
	return SSE{Data: map[string]any{"type": "response.output_message.content_part.done", "item_id": itemID, "content_index": 0, "part": map[string]any{"type": "output_text", "text": text}}}
}
func sseOutputItemDone(itemID, text string) SSE {
	if itemID == "" { panic("sseOutputItemDone: itemID required") }
	return SSE{Data: map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": itemID, "status": "completed"}}}
}
func sseCompletedText(text, itemID string) SSE {
	if text == "" || itemID == "" { panic("sseCompletedText: text and itemID required") }
	return SSE{Data: map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "id": itemID, "content": []any{map[string]any{"type": "output_text", "text": text}}}}}}}
}
