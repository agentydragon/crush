package middleware

import "github.com/charmbracelet/crush/internal/message"

// Middleware composes cross-cutting behavior around a message.Service.
// Use Compose(base, WithSessionSerialization(), WithDebounce(...)).
type Middleware func(message.Service) message.Service

// Compose applies middlewares left-to-right: Compose(base, m1, m2) == m2(m1(base)).
func Compose(base message.Service, mws ...Middleware) message.Service {
	out := base
	for i := 0; i < len(mws); i++ {
		out = mws[i](out)
	}
	return out
}
