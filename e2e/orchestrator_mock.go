package e2e

type mockOrchestrator struct{ s *mockResponsesServer }

func newMockOrchestrator(s *mockResponsesServer) *mockOrchestrator { return &mockOrchestrator{s: s} }

func (o *mockOrchestrator) Advance() {}

func (o *mockOrchestrator) EmitTextDelta(text string) {
	o.s.Enqueue(Step{Do: []Action{{Emit: []SSE{{Data: map[string]any{"type": "response.output_text.delta", "delta": text, "item_id": "out1"}}}}}})
}

func (o *mockOrchestrator) EmitCompleted(content string) {
	o.s.Enqueue(Step{Do: []Action{{Emit: []SSE{
		{Data: map[string]any{"type": "response.output_text.done"}},
		{Data: map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"status": "completed",
				"output": []any{
					map[string]any{
						"type": "message",
						"role": "assistant",
						"content": []any{map[string]any{"type": "output_text", "text": content}},
					},
				},
			},
		}},}})
}
}

func (o *mockOrchestrator) Close() { o.s.Enqueue(Step{Do: []Action{{Close: true}}}) }
