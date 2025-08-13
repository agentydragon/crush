package config

import (
	"bytes"
	"encoding/json"
	"io"
)

type raw = map[string]any

func deepMerge(dst, src raw) raw {
	for k, v := range src {
		if v == nil {
			delete(dst, k)
			continue
		}
		if dv, ok := dst[k]; ok {
			m1, ok1 := dv.(map[string]any)
			m2, ok2 := v.(map[string]any)
			if ok1 && ok2 {
				dst[k] = deepMerge(m1, m2)
				continue
			}
		}
		dst[k] = v
	}
	return dst
}

func Merge(data []io.Reader) (io.Reader, error) {
	merged := raw{}
	dec := json.NewDecoder(nil)
	for _, r := range data {
		var m raw
		dec = json.NewDecoder(r)
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			return nil, err
		}
		merged = deepMerge(merged, m)
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(out), nil
}
