package main

import (
	"bufio"
	"fmt"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A waveform is read by sampling each node's AXI-Stream ports on the clock, joining beats into
// messages, pairing received messages with sent ones, and decoding them with the profile.

// Port names the signals of one AXI-Stream port, relative to the node's scope.
type Port struct {
	Valid  string            `json:"valid"`
	Ready  string            `json:"ready"` // optional: always ready
	Data   string            `json:"data"`  // up to 64 bits, byte 0 in bits 7:0
	Keep   string            `json:"keep"`  // optional: every byte valid
	Last   string            `json:"last"`
	Dst    string            `json:"dst"`    // tx: destination address, matched against node addrs
	Error  string            `json:"error"`  // rx: frame error, sampled with the last beat
	Drop   string            `json:"drop"`   // rx: pulses when the node discards what it just got
	Fields map[string]string `json:"fields"` // tx: more signals recorded with each message
}

type WaveNode struct {
	Node  string `json:"node"`
	Label string `json:"label"`
	Scope string `json:"scope"`
	Addr  string `json:"addr"` // hex value of the tx dst signal meaning this node
}

// WaveNote turns a condition on a node's signals into a note, e.g. an application handshake.
type WaveNote struct {
	When  []string `json:"when"` // signals that must be 1 on the clock edge ("!name": 0)
	Text  string   `json:"text"` // template over signal values
	Count string   `json:"count"`
	Flow  string   `json:"flow"` // signal holding the flow key
}

type WaveConfig struct {
	Clock string     `json:"clock"` // full name, e.g. top.clk
	Nodes []WaveNode `json:"nodes"`
	TX    Port       `json:"tx"`
	RX    Port       `json:"rx"`
	Notes []WaveNote `json:"notes"`
}

type message struct {
	node, dst int
	addr      uint64
	t0, dropT float64
	data      []byte
	fields    map[string]any
	bad, drop bool
}

type noteSigs struct {
	cfg  WaveNote
	when []int
	neg  []bool
	vars map[string]int
	flow int
}

type nodeSigs struct {
	tv, tr, td, tk, tl, tdst      int
	rv, rr, rd, rk, rl, rerr, rdp int
	side                          map[string]int
	notes                         []noteSigs
	tx, rx                        *message
	lastRx, lastEdge, edges       int
}

type waveNote struct {
	t           float64
	node        int
	text, count string
	key         uint64
}

var templateVar = regexp.MustCompile(`\{([^}?!]+)`)

func readVCD(path string, cfg *WaveConfig, pf Profile) (*Trace, error) {
	if pf.Decode == nil {
		return nil, fmt.Errorf("reading a waveform needs profile.decode in waterski.json")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(bufio.NewReaderSize(f, 1<<20))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	sc.Split(bufio.ScanWords)
	next := func() string {
		if sc.Scan() {
			return sc.Text()
		}
		return ""
	}
	skip := func() {
		for t := next(); t != "" && t != "$end"; t = next() {
		}
	}

	ids, widths := map[string]string{}, map[string]int{}
	var scope []string
	unit, mult := "s", 1.0
header:
	for {
		switch tok := next(); tok {
		case "":
			return nil, fmt.Errorf("%s: no $enddefinitions", path)
		case "$timescale":
			var ts string
			for t := next(); t != "" && t != "$end"; t = next() {
				ts += t
			}
			num := strings.TrimRight(ts, "fpnums")
			unit = strings.Replace(ts[len(num):], "us", "µs", 1)
			if mult, err = strconv.ParseFloat(num, 64); err != nil {
				return nil, fmt.Errorf("%s: bad $timescale %q", path, ts)
			}
		case "$scope":
			next()
			scope = append(scope, next())
			skip()
		case "$upscope":
			scope = scope[:max(len(scope)-1, 0)]
			skip()
		case "$var":
			next()
			w, _ := strconv.Atoi(next())
			code, name := next(), next()
			skip()
			ids[strings.Join(scope, ".")+"."+name], widths[code] = code, w
		case "$enddefinitions":
			skip()
			break header
		default:
			if strings.HasPrefix(tok, "$") {
				skip()
			}
		}
	}

	index := map[string]int{}
	var vals []uint64
	var known []bool // false while x or z: a clock rises only from a known 0 to a known 1
	var width []int
	resolve := func(scope, name string) (int, error) {
		if name == "" {
			return -1, nil
		}
		full := name
		if scope != "" {
			full = scope + "." + name
		}
		code, ok := ids[full]
		if !ok {
			return -1, fmt.Errorf("%s: no signal %s", path, full)
		}
		if widths[code] > 64 {
			return -1, fmt.Errorf("%s: %s is %d bits; waterski reads signals of up to 64", path, full, widths[code])
		}
		if i, ok := index[code]; ok {
			return i, nil
		}
		index[code] = len(vals)
		vals, known, width = append(vals, 0), append(known, false), append(width, widths[code])
		return len(vals) - 1, nil
	}

	nodes := make([]*nodeSigs, len(cfg.Nodes))
	clk, err := resolve("", cfg.Clock)
	if err != nil || clk < 0 {
		return nil, fmt.Errorf("wave: needs a clock: %v", err)
	}
	addrs := map[uint64]int{}
	for i, wn := range cfg.Nodes {
		n := &nodeSigs{lastRx: -1, side: map[string]int{}}
		s := wn.Scope
		for _, r := range []struct {
			dst  *int
			name string
		}{
			{&n.tv, cfg.TX.Valid}, {&n.tr, cfg.TX.Ready}, {&n.td, cfg.TX.Data}, {&n.tk, cfg.TX.Keep},
			{&n.tl, cfg.TX.Last}, {&n.tdst, cfg.TX.Dst}, {&n.rv, cfg.RX.Valid}, {&n.rr, cfg.RX.Ready},
			{&n.rd, cfg.RX.Data}, {&n.rk, cfg.RX.Keep}, {&n.rl, cfg.RX.Last}, {&n.rerr, cfg.RX.Error},
			{&n.rdp, cfg.RX.Drop},
		} {
			if *r.dst, err = resolve(s, r.name); err != nil {
				return nil, err
			}
		}
		for _, req := range []int{n.tv, n.td, n.tl, n.rv, n.rd, n.rl} {
			if req < 0 {
				return nil, fmt.Errorf("wave: tx and rx need valid, data and last signals")
			}
		}
		for name, sig := range cfg.TX.Fields {
			if n.side[name], err = resolve(s, sig); err != nil {
				return nil, err
			}
		}
		for _, nc := range cfg.Notes {
			ns := noteSigs{cfg: nc, vars: map[string]int{}}
			for _, w := range nc.When {
				j, err := resolve(s, strings.TrimPrefix(w, "!"))
				if err != nil {
					return nil, err
				}
				if j < 0 {
					return nil, fmt.Errorf("wave: empty signal name in notes.when")
				}
				ns.when, ns.neg = append(ns.when, j), append(ns.neg, strings.HasPrefix(w, "!"))
			}
			for _, m := range templateVar.FindAllStringSubmatch(nc.Text, -1) {
				if ns.vars[m[1]], err = resolve(s, m[1]); err != nil {
					return nil, err
				}
			}
			if ns.flow, err = resolve(s, nc.Flow); err != nil {
				return nil, err
			}
			n.notes = append(n.notes, ns)
		}
		if wn.Addr != "" {
			a, err := strconv.ParseUint(wn.Addr, 16, 64)
			if err != nil {
				return nil, fmt.Errorf("wave: node %s: bad addr %q", wn.Node, wn.Addr)
			}
			addrs[a] = i
		}
		nodes[i] = n
	}

	on := func(i int) bool { return i < 0 || vals[i] != 0 }
	take := func(data, keep int) []byte {
		var out []byte
		for b := 0; b < width[data]/8; b++ {
			if keep < 0 || vals[keep]>>b&1 != 0 {
				out = append(out, byte(vals[data]>>(8*b)))
			}
		}
		return out
	}
	var txs, rxs []*message
	var notes []waveNote
	edge := func(i int, t float64) {
		n := nodes[i]
		n.edges++
		if vals[n.tv] != 0 && on(n.tr) {
			if n.tx == nil {
				n.tx = &message{node: i, dst: -1, t0: t, fields: map[string]any{}}
				if n.tdst >= 0 {
					n.tx.addr = vals[n.tdst]
					if d, ok := addrs[n.tx.addr]; ok {
						n.tx.dst = d
					}
				}
				for name, j := range n.side {
					n.tx.fields[name] = vals[j]
				}
			}
			n.tx.data = append(n.tx.data, take(n.td, n.tk)...)
			if vals[n.tl] != 0 {
				txs, n.tx = append(txs, n.tx), nil
			}
		}
		if vals[n.rv] != 0 && on(n.rr) {
			if n.rx == nil {
				n.rx = &message{node: i, t0: t}
			}
			n.rx.data = append(n.rx.data, take(n.rd, n.rk)...)
			if vals[n.rl] != 0 {
				n.rx.bad = n.rerr >= 0 && vals[n.rerr] != 0
				rxs, n.rx = append(rxs, n.rx), nil
				n.lastRx, n.lastEdge = len(rxs)-1, n.edges
			}
		}
		// a drop pulse on the last beat's edge or within 4 edges after it discards that message
		if n.rdp >= 0 && vals[n.rdp] != 0 && n.lastRx >= 0 && n.edges-n.lastEdge <= 4 && !rxs[n.lastRx].drop {
			rxs[n.lastRx].drop, rxs[n.lastRx].dropT = true, t
		}
		for _, ns := range n.notes {
			hit := true
			for k, j := range ns.when {
				hit = hit && (vals[j] != 0) != ns.neg[k]
			}
			if !hit {
				continue
			}
			f := map[string]any{}
			for name, j := range ns.vars {
				f[name] = vals[j]
			}
			nt := waveNote{t: t, node: i, text: expand(ns.cfg.Text, f), count: ns.cfg.Count}
			if ns.flow >= 0 {
				nt.key = vals[ns.flow]
			}
			notes = append(notes, nt)
		}
	}

	// values change at a timestamp after the clock edge there, so edges see the values before it
	type change struct {
		i int
		v uint64
		x bool // some bit is x or z (read as 0)
	}
	var pending []change
	t := 0.0
	flush := func() {
		for _, c := range pending {
			if c.i == clk && known[c.i] && vals[c.i] == 0 && !c.x && c.v == 1 {
				for i := range nodes {
					edge(i, t*mult)
				}
			}
		}
		for _, c := range pending {
			vals[c.i], known[c.i] = c.v, !c.x
		}
		pending = pending[:0]
	}
	for sc.Scan() {
		tok := sc.Text()
		switch c := tok[0]; c {
		case '#':
			flush()
			if t, err = strconv.ParseFloat(tok[1:], 64); err != nil {
				return nil, fmt.Errorf("%s: bad time %q", path, tok)
			}
		case 'b', 'B':
			if i, ok := index[next()]; ok {
				var v uint64
				x := false
				for _, ch := range tok[max(1, len(tok)-64):] {
					v = v<<1 | uint64(ch-'0')&1
					x = x || (ch != '0' && ch != '1')
				}
				pending = append(pending, change{i, v, x})
			}
		case 'r', 'R':
			next()
		case '$':
			if tok == "$comment" {
				skip()
			}
		default:
			if i, ok := index[tok[1:]]; ok {
				pending = append(pending, change{i, uint64(c-'0') & 1, c != '0' && c != '1'})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flush()
	return waveTrace(path, cfg, pf, txs, rxs, notes, unit)
}

type delivery struct {
	rx      int
	corrupt bool
}

// pair returns the received copies of each sent message: the earliest identical message to that
// node not yet received (all received: a duplicate), else the closest of the same length (corrupted).
func pair(txs, rxs []*message) [][]delivery {
	key := func(dst int, b []byte) string { return strconv.Itoa(dst) + "|" + string(b) }
	byKey := map[string][]int{}
	for i, m := range txs {
		byKey[key(m.dst, m.data)] = append(byKey[key(m.dst, m.data)], i)
	}
	got := make([][]delivery, len(txs))
	for j, r := range rxs {
		best := -1
		for _, i := range byKey[key(r.node, r.data)] {
			if txs[i].t0 > r.t0 {
				break
			}
			best = i
			if len(got[i]) == 0 {
				break
			}
		}
		corrupt := false
		if best < 0 {
			fewest := math.MaxInt
			for i, m := range txs {
				if m.dst != r.node || len(m.data) != len(r.data) || m.t0 > r.t0 {
					continue
				}
				d := 0
				for k := range m.data {
					d += bits.OnesCount8(m.data[k] ^ r.data[k])
				}
				if d < fewest || (d == fewest && len(got[i]) == 0 && len(got[best]) > 0) {
					best, fewest = i, d
				}
			}
			corrupt = best >= 0
		}
		if best >= 0 {
			got[best] = append(got[best], delivery{j, corrupt})
		}
	}
	return got
}

func waveTrace(path string, cfg *WaveConfig, pf Profile, txs, rxs []*message, notes []waveNote, unit string) (*Trace, error) {
	got := pair(txs, rxs)
	mask := ^uint64(0) // notes' flow signals hold the same key as messages
	if _, m, ok := strings.Cut(pf.Decode.Flow, "&"); ok {
		mask, _ = strconv.ParseUint(m, 16, 64)
	}
	flow := func(k uint64) string {
		if k&mask == 0 {
			return ""
		}
		return strconv.FormatUint(k&mask, 16)
	}
	name := func(i int, addr uint64) string {
		if i < 0 {
			return fmt.Sprintf("%x", addr)
		}
		return cfg.Nodes[i].Node
	}

	evs := []event{{Ev: "meta", Title: filepath.Base(path)}}
	for _, wn := range cfg.Nodes {
		evs = append(evs, event{Ev: "node", Node: wn.Node, Label: wn.Label})
	}
	id := int64(0)
	paired := make([]bool, len(rxs))
	for i, m := range txs {
		typ, key, fields, err := pf.message(m.data)
		if err != nil {
			return nil, err
		}
		for k, v := range m.fields {
			fields[k] = v
		}
		send := event{Ev: "send", T: m.t0, Src: name(m.node, 0), Type: typ, Flow: flow(key), Fields: fields}
		if len(got[i]) == 0 {
			send.ID, send.Dst = id, name(m.dst, m.addr)
			evs = append(evs, send, event{Ev: "drop", T: m.t0, ID: id, Reason: "never arrived"})
			id++
		}
		for k, d := range got[i] {
			r := rxs[d.rx]
			paired[d.rx] = true
			var note []string
			if k > 0 {
				note = append(note, "network duplicate")
			}
			if d.corrupt {
				note = append(note, "corrupted in the network")
			}
			send.ID, send.Dst, send.Note = id, name(r.node, 0), strings.Join(note, ", ")
			evs = append(evs, send, event{Ev: "recv", T: r.t0, ID: id})
			if r.drop {
				reason := "rejected by the receiver"
				if r.bad {
					reason = "frame error: the receiver discarded it"
				}
				evs = append(evs, event{Ev: "drop", T: r.dropT, ID: id, Reason: reason})
			}
			id++
		}
	}
	for _, n := range notes {
		evs = append(evs, event{Ev: "note", T: n.t, Node: name(n.node, 0), Flow: flow(n.key), Text: n.text, Count: n.count})
	}
	for j, r := range rxs {
		if !paired[j] {
			evs = append(evs, event{Ev: "note", T: r.t0, Node: name(r.node, 0), Count: "unexplained arrivals",
				Text: fmt.Sprintf("received %d bytes that no node sent", len(r.data))})
		}
	}

	// in time order (a message is never received before it is sent), flows numbered as they appear
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].T < evs[j].T })
	labels := map[string]string{}
	for k := range evs {
		if f := evs[k].Flow; f != "" {
			if labels[f] == "" {
				labels[f] = strings.TrimSpace(fmt.Sprintf("%s %d", pf.Decode.FlowLabel, len(labels)))
			}
			evs[k].Flow = labels[f]
		}
	}

	// use the coarsest unit that keeps every time whole, e.g. ps multiples of 1000 as ns
	u := 0
	for u < len(units) && units[u] != unit {
		u++
	}
	for u < len(units)-1 {
		whole := true
		for _, e := range evs {
			whole = whole && math.Mod(e.T, 1000) == 0
		}
		if !whole {
			break
		}
		for k := range evs {
			evs[k].T /= 1000
		}
		u++
	}
	if u < len(units) {
		unit = units[u]
	}
	evs[0].Unit = unit
	return buildTrace(evs)
}
