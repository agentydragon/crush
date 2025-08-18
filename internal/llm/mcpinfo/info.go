package mcpinfo

import "sync"

var (
	mu           sync.RWMutex
	instructions = map[string]string{}
)

func SetInstructions(name, text string) {
	if name == "" || text == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	instructions[name] = text
}

func GetAll() map[string]string {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[string]string, len(instructions))
	for k, v := range instructions {
		out[k] = v
	}
	return out
}
