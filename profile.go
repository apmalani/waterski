package main

import (
	"fmt"
	"hash/fnv"
	"math"
	"regexp"
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

	// for messages decoded from raw bytes (waveforms)
	Code    *uint64           `json:"code"`    // value of the type field
	Fields  map[string]string `json:"fields"`  // name: "offset:size" or "offset:size&hexmask", big-endian
	Compute map[string]string `json:"compute"` // name: expression over fields, see eval
}

// Profile describes one protocol's message types.
type Profile struct {
	Name   string           `json:"name"`
	Types  map[string]Style `json:"types"`
	Decode *Decode          `json:"decode"`
}

// Decode says how to read a message's raw bytes: its type, its flow, and fields every type has.
// Every message also gets "bytes", its length.
type Decode struct {
	Type      string            `json:"type"`       // "offset:size" of the type code
	Flow      string            `json:"flow"`       // field spec of a key shared by a flow's messages; 0 is none
	FlowLabel string            `json:"flow_label"` // flows are named "<label> 1", "<label> 2", ... as they appear
	Fields    map[string]string `json:"fields"`
}

// field reads a big-endian field spec "offset:size" or "offset:size&hexmask" from b.
func field(spec string, b []byte) (uint64, bool, error) {
	pos, mask, masked := strings.Cut(spec, "&")
	at, size, ok := strings.Cut(pos, ":")
	o, err1 := strconv.Atoi(at)
	n, err2 := strconv.Atoi(size)
	m := ^uint64(0)
	var err3 error
	if masked {
		m, err3 = strconv.ParseUint(mask, 16, 64)
	}
	if !ok || err1 != nil || err2 != nil || err3 != nil || o < 0 || n < 1 || n > 8 {
		return 0, false, fmt.Errorf("bad field %q: want offset:size or offset:size&hexmask", spec)
	}
	if o > len(b)-n {
		return 0, false, nil
	}
	var v uint64
	for _, c := range b[o : o+n] {
		v = v<<8 | uint64(c)
	}
	return v & m, true, nil
}

// message decodes one message: its type name, flow key and fields.
func (pf Profile) message(b []byte) (string, uint64, map[string]any, error) {
	d := pf.Decode
	code, ok, err := field(d.Type, b)
	if err != nil || !ok {
		return "", 0, nil, fmt.Errorf("decode type: %v (message of %d bytes)", err, len(b))
	}
	typ, st := fmt.Sprintf("TYPE_%d", code), Style{}
	for name, s := range pf.Types {
		if s.Code != nil && *s.Code == code {
			typ, st = name, s
		}
	}
	var key uint64
	if d.Flow != "" {
		if key, _, err = field(d.Flow, b); err != nil {
			return "", 0, nil, err
		}
	}
	f := map[string]any{"bytes": uint64(len(b))}
	for _, specs := range []map[string]string{d.Fields, st.Fields} {
		for name, spec := range specs {
			v, ok, err := field(spec, b)
			if err != nil {
				return "", 0, nil, fmt.Errorf("%s.%s: %v", typ, name, err)
			}
			if ok {
				f[name] = v
			}
		}
	}
	compute(st.Compute, f)
	return typ, key, f, nil
}

var (
	sum  = regexp.MustCompile(`^\s*\w+(\s*[-+]\s*\w+)+\s*$`)
	term = regexp.MustCompile(`[-+]|\w+`)
	word = regexp.MustCompile(`\w+`)
)

// compute adds derived fields, each once every derived field it names is known.
func compute(exprs map[string]string, f map[string]any) {
	left := map[string]string{}
	for k, x := range exprs {
		left[k] = x
	}
	for progress := true; progress; {
		progress = false
		for k, x := range left {
			ready := true
			for _, w := range word.FindAllString(x, -1) {
				_, pending := left[w]
				ready = ready && (!pending || w == k)
			}
			if ready {
				if v, ok := eval(x, f); ok {
					f[k] = v
				}
				delete(left, k)
				progress = true
			}
		}
	}
}

// eval computes a derived field: a sum ("off+payload-1", none if a field is missing), a choice
// ("S?a:b" or "len=0?a:b", branches are templates), or a template ("{c0} {c1}").
func eval(x string, f map[string]any) (any, bool) {
	if sum.MatchString(x) {
		var total int64
		sign := int64(1)
		for _, t := range term.FindAllString(x, -1) {
			switch t {
			case "+":
				sign = 1
			case "-":
				sign = -1
			default:
				n, err := strconv.ParseInt(t, 10, 64)
				if err != nil {
					v, ok := f[t]
					if !ok {
						return nil, false
					}
					if n, err = strconv.ParseInt(format(v), 10, 64); err != nil {
						return nil, false
					}
				}
				total += sign * n
			}
		}
		return total, true
	}
	if cond, rest, ok := strings.Cut(x, "?"); ok && !strings.ContainsAny(cond, "{} ") {
		yes, no, _ := strings.Cut(rest, ":")
		name, want, eq := strings.Cut(cond, "=")
		v, has := f[name]
		if (eq && has && format(v) == want) || (!eq && truthy(v)) {
			return expand(yes, f), true
		}
		return expand(no, f), true
	}
	return expand(x, f), true
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
	case uint64:
		return v != 0
	case int64:
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
