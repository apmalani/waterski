package main

import (
	"strings"
	"testing"
)

const sample = `{"ev":"meta","title":"ping","time_unit":"ns"}
{"ev":"node","node":"a","label":"Board A"}
{"ev":"send","t":10,"id":1,"src":"a","dst":"b","type":"REQ","flow":"x","fields":{"n":3,"retry":0}}
{"ev":"recv","t":30,"id":1}
{"ev":"note","t":31,"node":"b","flow":"x","count":"served","text":"got it"}
{"ev":"send","t":40,"id":2,"src":"b","dst":"a","type":"RSP","flow":"x","fields":{"n":3}}
{"ev":"drop","t":40,"id":2,"reason":"lost"}
{"ev":"send","t":50,"id":3,"src":"b","dst":"c","type":"RSP","fields":{}}
`

func TestParse(t *testing.T) {
	tr, err := parseTrace(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Nodes) != 3 || tr.Nodes[0].title() != "Board A · a" || tr.Nodes[1].title() != "b" {
		t.Fatalf("nodes: %+v", tr.Nodes)
	}
	if tr.Flight != 20 || len(tr.Entries) != 5 || len(tr.Flows) != 1 || tr.End != 70 {
		t.Fatalf("flight %v entries %d flows %v end %v", tr.Flight, len(tr.Entries), tr.Flows, tr.End)
	}
	if p := tr.Packets[1]; !p.Dropped || p.Arrived || p.Reason != "lost" {
		t.Fatalf("drop: %+v", p)
	}
	for _, bad := range []string{`{"ev":"recv","id":9}`, `{"ev":"send","id":1,"src":"a"}`, `{"ev":"bogus"}`, `{`} {
		if _, err := parseTrace(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestExpand(t *testing.T) {
	f := map[string]any{"off": 64.0, "len": 474.0, "retx": 0.0, "msg": "response", "ack": "1f"}
	cases := map[string]string{
		"{msg} {off}..{len}":     "response 64..474",
		"x{retx? (again)}y":      "xy",
		"x{ack? +ack}{missing}y": "x +acky",
		"no braces":              "no braces",
		"unclosed {off":          "unclosed {off",
	}
	for in, want := range cases {
		if got := expand(in, f); got != want {
			t.Errorf("expand(%q) = %q, want %q", in, got, want)
		}
	}
	if got := fieldList(map[string]any{"b": 2.0, "a": "x"}); got != "a=x b=2" {
		t.Errorf("fieldList = %q", got)
	}
}

func TestFramesDoNotPanic(t *testing.T) {
	tr, err := parseTrace(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(tr, Profile{})
	for _, size := range [][2]int{{120, 40}, {40, 16}, {10, 5}, {300, 80}} {
		for _, now := range []float64{0, 15, 35, 45, 70} {
			m.w, m.h, m.now = size[0], size[1], now
			for _, sel := range []int{-1, 0, 3} {
				m.sel = sel
				if m.View() == "" {
					t.Fatalf("empty frame at %v", size)
				}
			}
		}
	}
}

func TestFmtTime(t *testing.T) {
	cases := []struct {
		t, scale float64
		want     string
	}{{3420, 9400, "3.420 µs"}, {585, 900, "585 ns"}, {12.5, 50, "12.5 ns"}, {7, 7, "7 cycles"}, {2e6, 5e6, "2000.000 µs"}}
	for _, c := range cases {
		unit := "ns"
		if c.want == "7 cycles" {
			unit = "cycles"
		}
		if got := fmtTime(c.t, unit, c.scale); got != c.want {
			t.Errorf("fmtTime(%v, %v) = %q, want %q", c.t, c.scale, got, c.want)
		}
	}
}
