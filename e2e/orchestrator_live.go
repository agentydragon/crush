package e2e

type liveOrchestrator struct{}

func newLiveOrchestrator() *liveOrchestrator { return &liveOrchestrator{} }

func (o *liveOrchestrator) Advance()                 {}
func (o *liveOrchestrator) EmitTextDelta(string)     {}
func (o *liveOrchestrator) EmitCompleted(string)     {}
func (o *liveOrchestrator) Close()                   {}
