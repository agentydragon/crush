package types

import "time"

type ConditionKind int

const (
	CondNone ConditionKind = iota
	CondRequestBodyContains
	CondSignal
	CondSleep
)

type Condition struct {
	Kind     ConditionKind
	Name     string
	Duration time.Duration
}

type SSE struct{ Data any }

type Action struct {
	Emit  []SSE
	Close bool
}

type Step struct {
	WaitUntil []Condition
	Do        []Action
}
