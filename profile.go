package main

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Style says how one message type looks. Short and Log are templates over the packet's fields:
// {key} is the field's value and {key?text} is text when the field is present and not zero.
type Style struct {
	Color string `json:"color"`
	Short string `json:"short"` // label in the diagram
	Log   string `json:"log"`   // sentence in the log
	About string `json:"about"` // what this message type is for
}

// Profile describes one protocol's message types.
type Profile struct {
	Name  string           `json:"name"`
	Types map[string]Style `json:"types"`
}

var palette = []string{"#4da3ff", "#3ddc84", "#ffa94d", "#e599f7", "#ffd43b", "#b197fc", "#63e6be", "#ff8787"}

func (pf Profile) style(typ string) Style {
	s := pf.Types[typ]
	if s.Color == "" {
		h := fnv.New32a()
		h.Write([]byte(typ))
		s.Color = palette[h.Sum32()%uint32(len(palette))]
	}
	if s.Short == "" {
		s.Short = typ
	}
	return s
}

// describe is the log sentence for a packet: the profile's template, or its fields as key=value.
func (pf Profile) describe(p *Packet) string {
	if t := pf.style(p.Type).Log; t != "" {
		return expand(t, p.Fields)
	}
	return fieldList(p.Fields)
}

func expand(tmpl string, f map[string]any) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(tmpl, '{')
		j := strings.IndexByte(tmpl[max(i, 0):], '}')
		if i < 0 || j < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		b.WriteString(tmpl[:i])
		key, text, cond := strings.Cut(tmpl[i+1:i+j], "?")
		if v, ok := f[key]; ok && (!cond || truthy(v)) {
			if cond {
				b.WriteString(text)
			} else {
				b.WriteString(format(v))
			}
		}
		tmpl = tmpl[i+j+1:]
	}
}

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != "" && v != "0"
	}
	return true
}

func format(v any) string {
	switch v := v.(type) {
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1e15 {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'g', -1, 64)
	case string:
		return v
	}
	return fmt.Sprint(v)
}

func fieldList(f map[string]any) string {
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + format(f[k])
	}
	return strings.Join(parts, " ")
}
