package profile

import "sync/atomic"

var addr atomic.Value

func SetAddr(address string) { addr.Store(address) }

func Addr() string {
	stored := addr.Load()
	if stored == nil {
		return ""
	}
	if s, ok := stored.(string); ok {
		return s
	}
	return ""
}
