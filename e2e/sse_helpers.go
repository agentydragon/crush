package e2e

// Helper to construct correct function_call SSE messages per OpenAI SDK spec.
// All parameters are REQUIRED; this will panic in tests if any are empty.
func sseFunctionCallAdded(id, callID, name, args, status string) SSE {
	if id == "" || callID == "" || name == "" || args == "" || status == "" {
		panic("sseFunctionCallAdded: id, callID, name, args, status are all required and must be non-empty")
	}
	return SSE{Data: map[string]any{
		"type":             "response.output_item.added",
		"sequence_number":  3,
		"output_index":     0,
		"item": map[string]any{
			"type":      "function_call",
			"id":        id,
			"call_id":   callID,
			"name":      name,
			"arguments": args,
			"status":    status,
		},
	}}
}

// Helper for function_call in response.completed[output], status=completed.
// All parameters are REQUIRED; this will panic if any are empty.
func sseFunctionCallFinal(id, callID, name, args string) map[string]any {
	if id == "" || callID == "" || name == "" || args == "" {
		panic("sseFunctionCallFinal: id, callID, name, args are all required and must be non-empty")
	}
	return map[string]any{
		"type": "function_call",
		"id": id,
		"call_id": callID,
		"name": name,
		"arguments": args,
		"status": "completed",
	}
}
