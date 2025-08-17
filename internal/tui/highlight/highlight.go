package highlight

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/color"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	chromaStyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/crush/internal/tui/styles"
)

const highlightCacheCap = 64

type hlEntry struct {
	key   string
	value string
}

var (
	hlMu    sync.Mutex
	hlIndex = map[string]*list.Element{}
	hlList  = list.New()
)

func bgRGB(bg color.Color) (uint8, uint8, uint8) {
	r, g, b, _ := bg.RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func makeKey(source, fileName string, bg color.Color) string {
	r, g, b := bgRGB(bg)
	// Theme influences style selection; include it
	t := styles.CurrentTheme()
	return fmt.Sprintf("%s|%s|%02x%02x%02x|%s|%t", hashString(source), fileName, r, g, b, t.Name, t.IsDark)
}

func hlGet(key string) (string, bool) {
	if el, ok := hlIndex[key]; ok {
		hlList.MoveToFront(el)
		return el.Value.(hlEntry).value, true
	}
	return "", false
}

func hlPut(key, value string) {
	if el, ok := hlIndex[key]; ok {
		el.Value = hlEntry{key: key, value: value}
		hlList.MoveToFront(el)
		return
	}
	el := hlList.PushFront(hlEntry{key: key, value: value})
	hlIndex[key] = el
	if hlList.Len() > highlightCacheCap {
		old := hlList.Back()
		if old != nil {
			ent := old.Value.(hlEntry)
			delete(hlIndex, ent.key)
			hlList.Remove(old)
		}
	}
}

func SyntaxHighlight(source, fileName string, bg color.Color) (string, error) {
	key := makeKey(source, fileName, bg)
	hlMu.Lock()
	if v, ok := hlGet(key); ok {
		hlMu.Unlock()
		return v, nil
	}
	hlMu.Unlock()

	// Determine the language lexer to use
	l := lexers.Match(fileName)
	if l == nil {
		l = lexers.Analyse(source)
	}
	if l == nil {
		l = lexers.Fallback
	}
	l = chroma.Coalesce(l)

	// Get the formatter
	f := formatters.Get("terminal16m")
	if f == nil {
		f = formatters.Fallback
	}

	style := chroma.MustNewStyle("crush", styles.GetChromaTheme())

	// Modify the style to use the provided background
	s, err := style.Builder().Transform(
		func(t chroma.StyleEntry) chroma.StyleEntry {
			r, g, b, _ := bg.RGBA()
			t.Background = chroma.NewColour(uint8(r>>8), uint8(g>>8), uint8(b>>8))
			return t
		},
	).Build()
	if err != nil {
		s = chromaStyles.Fallback
	}

	// Tokenize and format
	it, err := l.Tokenise(nil, source)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = f.Format(&buf, s, it)
	out := buf.String()
	if err == nil {
		hlMu.Lock()
		hlPut(key, out)
		hlMu.Unlock()
	}
	return out, err
}
