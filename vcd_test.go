package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// waveSigs are the signals of each node in the test waveform: name and width.
var waveSigs = [][2]any{{"tv", 1}, {"td", 16}, {"tl", 1}, {"tdst", 8}, {"rv", 1}, {"rd", 16}, {"rl", 1},
	{"rerr", 1}, {"rdrop", 1}, {"go", 1}, {"len", 8}, {"k", 8}}

// testWave builds a VCD of two nodes moving 2-byte beats. Values for clock edge c are set at the
// time of edge c-1, as registers would, so the reader must sample the values from before an edge.
type testWave struct{ cyc []map[string]uint64 }

func (w *testWave) set(c int, node string, kv ...any) {
	for len(w.cyc) <= c+1 {
		w.cyc = append(w.cyc, map[string]uint64{})
	}
	for i := 0; i < len(kv); i += 2 {
		w.cyc[c][node+"."+kv[i].(string)] = uint64(kv[i+1].(int))
	}
}

func (w *testWave) beats(c int, node, dir string, b []byte, extra ...any) {
	for i := 0; i < len(b); i += 2 {
		v := int(b[i]) | int(b[i+1])<<8
		w.set(c+i/2, node, append([]any{dir + "v", 1, dir + "d", v, dir + "l", btoi(i+2 >= len(b))}, extra...)...)
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (w *testWave) vcd() string {
	var b strings.Builder
	b.WriteString("$date today $end\n$timescale 1ns $end\n$scope module top $end\n$var wire 1 ! clk $end\n")
	var names []string
	for _, node := range []string{"a", "b"} {
		fmt.Fprintf(&b, "$scope module %s $end\n", node)
		for _, s := range waveSigs {
			fmt.Fprintf(&b, "$var wire %d %s.%s %s [%d:0] $end\n", s[1], node, s[0], s[0], s[1].(int)-1)
			names = append(names, node+"."+s[0].(string))
		}
		b.WriteString("$upscope $end\n")
	}
	b.WriteString("$upscope $end\n$enddefinitions $end\n#0\n$dumpvars\n0!\n")
	for _, n := range names {
		fmt.Fprintf(&b, "bx %s\n", n)
	}
	b.WriteString("$end\n")
	prev := map[string]uint64{}
	emit := func(c int) {
		for _, n := range names {
			if v := w.cyc[c][n]; c == 1 || v != prev[n] {
				fmt.Fprintf(&b, "b%b %s\n", v, n)
				prev[n] = v
			}
		}
	}
	b.WriteString("#1\n")
	emit(1)
	for c := 1; c+1 < len(w.cyc); c++ {
		fmt.Fprintf(&b, "#%d\n1!\n", 10*c)
		emit(c + 1)
		fmt.Fprintf(&b, "#%d\n0!\n", 10*c+5)
	}
	return b.String()
}

func TestVCD(t *testing.T) {
	var w testWave
	// a -> b: delivered; then lost; then delivered and rejected
	w.beats(2, "a", "t", []byte{1, 5, 7, 0}, "tdst", 0xb)
	w.beats(4, "b", "r", []byte{1, 5, 7, 0})
	w.beats(6, "a", "t", []byte{2, 5, 9, 0}, "tdst", 0xb)
	w.beats(10, "a", "t", []byte{1, 5, 8, 8}, "tdst", 0xb)
	w.beats(12, "b", "r", []byte{1, 5, 8, 8})
	w.set(14, "b", "rdrop", 1)
	// b -> a: duplicated; then corrupted, flagged and discarded
	w.beats(2, "b", "t", []byte{1, 6, 3, 0}, "tdst", 0xa)
	w.beats(5, "a", "r", []byte{1, 6, 3, 0})
	w.beats(8, "a", "r", []byte{1, 6, 3, 0})
	w.beats(10, "b", "t", []byte{2, 6, 4, 0}, "tdst", 0xa)
	w.beats(12, "a", "r", []byte{2, 6, 4, 1}, "rerr", 1)
	w.set(14, "a", "rdrop", 1)
	// a beat no one sent, and an app event
	w.beats(16, "b", "r", []byte{9, 9})
	w.set(1, "a", "go", 1, "len", 4, "k", 5)
	w.set(18, "a")

	path := filepath.Join(t.TempDir(), "t.vcd")
	// the clock going unknown and back to 1 (as around $dumpoff/$dumpon) is not a rising edge
	if err := os.WriteFile(path, []byte(w.vcd()+"#200\nx!\nb1 a.go\n#210\n1!\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	one, two := uint64(1), uint64(2)
	pf := Profile{
		Decode: &Decode{Type: "0:1", Flow: "1:1", FlowLabel: "F"},
		Types: map[string]Style{
			"REQ": {Code: &one, Fields: map[string]string{"n": "2:1"}, Compute: map[string]string{"m": "n+bytes-1", "s": "m=11?big {m}:small"}},
			"RSP": {Code: &two},
		},
	}
	port := func(p string) Port {
		return Port{Valid: p + "v", Data: p + "d", Last: p + "l", Dst: "tdst", Error: "rerr", Drop: "rdrop"}
	}
	cfg := &WaveConfig{
		Clock: "top.clk",
		Nodes: []WaveNode{{Node: "a", Scope: "top.a", Addr: "a"}, {Node: "b", Scope: "top.b", Addr: "b"}},
		TX:    port("t"), RX: port("r"),
		Notes: []WaveNote{{When: []string{"go", "!tv"}, Text: "go {len}", Count: "gos", Flow: "k"}},
	}
	tr, err := readVCD(path, cfg, pf)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range tr.Packets {
		s := fmt.Sprintf("%v %s>%s %s %s sent=%v", p.Sent, p.Src, p.Dst, p.Type, p.Flow, p.Sent)
		if p.Arrived {
			s += fmt.Sprintf(" recv=%v", p.Recv)
		}
		if p.Note != "" {
			s += " [" + p.Note + "]"
		}
		if p.Dropped {
			s += fmt.Sprintf(" drop=%v %s", p.Drop, p.Reason)
		}
		got = append(got, s)
	}
	sort.Strings(got)
	want := []string{
		"100 a>b REQ F 0 sent=100 recv=120 drop=140 rejected by the receiver",
		"100 b>a RSP F 1 sent=100 recv=120 [corrupted in the network] drop=140 frame error: the receiver discarded it",
		"20 a>b REQ F 0 sent=20 recv=40",
		"20 b>a REQ F 1 sent=20 recv=50",
		"20 b>a REQ F 1 sent=20 recv=80 [network duplicate]",
		"60 a>b RSP F 0 sent=60 drop=60 never arrived",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("packets:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if tr.Unit != "ns" || tr.Flows[0] != "F 0" {
		t.Errorf("unit %q flows %v", tr.Unit, tr.Flows)
	}
	f := tr.Packets[0].Fields
	if f["n"] != uint64(7) || f["bytes"] != uint64(4) || f["m"] != int64(10) || f["s"] != "small" {
		t.Errorf("fields: %v", f)
	}
	if f := tr.Packets[len(tr.Packets)-2].Fields; f["s"] != "big 11" {
		t.Errorf("fields of the last REQ: %v", f)
	}
	var notes []string
	for _, n := range tr.Notes {
		notes = append(notes, fmt.Sprintf("%v %s %s %s", n.T, n.Node, n.Flow, n.Text))
	}
	if strings.Join(notes, "; ") != "10 a F 0 go 4; 160 b  received 2 bytes that no node sent" {
		t.Errorf("notes: %v", notes)
	}

	cfg.Notes[0].When = []string{"!"}
	if _, err := readVCD(path, cfg, pf); err == nil {
		t.Errorf("accepted an empty condition")
	}
	cfg.TX.Valid = "nope"
	if _, err := readVCD(path, cfg, pf); err == nil || !strings.Contains(err.Error(), "top.a.nope") {
		t.Errorf("missing signal: %v", err)
	}
}

func TestEval(t *testing.T) {
	f := map[string]any{"off": uint64(64), "n": 10.0, "S": uint64(1), "rlen": uint64(4294967295), "msg": "x"}
	cases := map[string]any{
		"off+n-1":                           int64(73),
		"off - 100":                         int64(-36),
		"off+missing":                       nil,
		"S?response:request":                "response",
		"rlen=4294967295?all:bytes {off}..": "all",
		"rlen=5?all:bytes {off}..":          "bytes 64..",
		"missing?a:b":                       "b",
		"{msg} {off?has off}":               "x has off",
	}
	for x, want := range cases {
		v, ok := eval(x, f)
		if (want == nil) == ok || (ok && v != want) {
			t.Errorf("eval(%q) = %v, %v; want %v", x, v, ok, want)
		}
	}
	g := map[string]any{"a": uint64(2)}
	compute(map[string]string{"c": "b+1", "b": "a+1", "loop": "loop+1"}, g)
	if g["c"] != int64(4) || g["loop"] != nil {
		t.Errorf("compute: %v", g)
	}
	for _, bad := range []string{"1", "1:9", "x:1", "1:1&zz"} {
		if _, _, err := field(bad, []byte{1, 2}); err == nil {
			t.Errorf("field(%q) accepted", bad)
		}
	}
	if v, ok, _ := field("0:2&0fff", []byte{0xab, 0xcd}); !ok || v != 0x0bcd {
		t.Errorf("field = %x", v)
	}
	if _, ok, _ := field("1:2", []byte{1, 2}); ok {
		t.Errorf("field past the end")
	}
}
