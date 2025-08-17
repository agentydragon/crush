package profile

import "sync/atomic"

var addr atomic.Value

func SetAddr(s string) { addr.Store(s) }

func Addr() string {
	v := addr.Load()
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
