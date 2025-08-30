package e2e

import types "github.com/charmbracelet/crush/e2e/types"

// Re-export shared SSE typing in e2e to preserve existing references.

type (
	SSE       = types.SSE
	Action    = types.Action
	Step      = types.Step
	Condition = types.Condition
)

const (
	CondNone               = types.CondNone
	CondRequestBodyContains = types.CondRequestBodyContains
	CondSignal             = types.CondSignal
	CondSleep              = types.CondSleep
)
