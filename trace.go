package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// event is one line of a trace. Which keys are used depends on Ev.
type event struct {
	Ev     string         `json:"ev"`
	T      float64        `json:"t"`
	ID     int64          `json:"id"`
	Src    string         `json:"src"`
	Dst    string         `json:"dst"`
	Type   string         `json:"type"`
	Flow   string         `json:"flow"`
	Note   string         `json:"note"`
	Reason string         `json:"reason"`
	Node   string         `json:"node"`
	Label  string         `json:"label"`
	Text   string         `json:"text"`
	Count  string         `json:"count"`
	Title  string         `json:"title"`
	Unit   string         `json:"time_unit"`
	X      *float64       `json:"x"`
	Y      *float64       `json:"y"`
	Fields map[string]any `json:"fields"`
}

type Node struct {
	ID, Label string
	X, Y      float64
	Placed    bool
}

func (n *Node) title() string {
	if n.Label == "" || n.Label == n.ID {
		return n.ID
	}
	return n.Label + " · " + n.ID
}

type Packet struct {
	ID                   int64
	Src, Dst, Type, Flow string
	Note, Reason         string
	Fields               map[string]any
	Sent, Recv, Drop     float64
	Arrived, Dropped     bool
}

type Note struct {
	T                       float64
	Node, Flow, Text, Count string
}

// Entry is one line of the detailed log: a packet sent ('s'), a packet dropped ('d') or a note ('n').
type Entry struct {
	T    float64
	Kind byte
	P    *Packet
	N    *Note
}

func (e Entry) flow() string {
	if e.P != nil {
		return e.P.Flow
	}
	return e.N.Flow
}

type Trace struct {
	Title, Unit string
	Nodes       []*Node
	Packets     []*Packet
	Notes       []*Note
	Entries     []Entry
	Flows       []string
	End         float64
	Flight      float64 // median time from send to arrival
	nodes       map[string]*Node
}

func (tr *Trace) node(id string) *Node {
	n := tr.nodes[id]
	if n == nil {
		n = &Node{ID: id}
		tr.nodes[id] = n
		tr.Nodes = append(tr.Nodes, n)
	}
	return n
}

func parseTrace(r io.Reader) (*Trace, error) {
	var evs []event
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("line %d: %v", n, err)
		}
		evs = append(evs, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return buildTrace(evs)
}

// buildTrace turns events, in order, into the model the player shows.
func buildTrace(evs []event) (*Trace, error) {
	tr := &Trace{Unit: "ns", nodes: map[string]*Node{}}
	byID := map[int64]*Packet{}
	seen := map[string]bool{"": true}
	addFlow := func(f string) {
		if !seen[f] {
			seen[f] = true
			tr.Flows = append(tr.Flows, f)
		}
	}
	for i, e := range evs {
		n := i + 1
		switch e.Ev {
		case "meta":
			if e.Title != "" {
				tr.Title = e.Title
			}
			if e.Unit != "" {
				tr.Unit = e.Unit
			}
		case "node":
			if e.Node == "" {
				return nil, fmt.Errorf("event %d: node event without \"node\"", n)
			}
			nd := tr.node(e.Node)
			if e.Label != "" {
				nd.Label = e.Label
			}
			if e.X != nil && e.Y != nil {
				nd.X, nd.Y, nd.Placed = *e.X, *e.Y, true
			}
		case "send":
			if byID[e.ID] != nil {
				return nil, fmt.Errorf("event %d: packet id %d sent twice", n, e.ID)
			}
			if e.Src == "" || e.Dst == "" {
				return nil, fmt.Errorf("event %d: send needs \"src\" and \"dst\"", n)
			}
			tr.node(e.Src)
			tr.node(e.Dst)
			p := &Packet{ID: e.ID, Src: e.Src, Dst: e.Dst, Type: e.Type, Flow: e.Flow, Note: e.Note, Fields: e.Fields, Sent: e.T}
			byID[e.ID] = p
			tr.Packets = append(tr.Packets, p)
			tr.Entries = append(tr.Entries, Entry{T: e.T, Kind: 's', P: p})
			addFlow(e.Flow)
		case "recv", "drop":
			p := byID[e.ID]
			if p == nil {
				return nil, fmt.Errorf("event %d: %s for unknown packet id %d", n, e.Ev, e.ID)
			}
			if e.Ev == "recv" {
				p.Recv, p.Arrived = e.T, true
			} else {
				p.Drop, p.Dropped, p.Reason = e.T, true, e.Reason
				tr.Entries = append(tr.Entries, Entry{T: e.T, Kind: 'd', P: p})
			}
		case "note":
			if e.Node != "" {
				tr.node(e.Node)
			}
			nt := &Note{T: e.T, Node: e.Node, Flow: e.Flow, Text: e.Text, Count: e.Count}
			tr.Notes = append(tr.Notes, nt)
			tr.Entries = append(tr.Entries, Entry{T: e.T, Kind: 'n', N: nt})
			addFlow(e.Flow)
		default:
			return nil, fmt.Errorf("event %d: unknown event %q", n, e.Ev)
		}
		tr.End = max(tr.End, e.T)
	}
	sort.SliceStable(tr.Entries, func(i, j int) bool { return tr.Entries[i].T < tr.Entries[j].T })

	var flights []float64
	for _, p := range tr.Packets {
		if p.Arrived && p.Recv > p.Sent {
			flights = append(flights, p.Recv-p.Sent)
		}
	}
	if len(flights) > 0 {
		sort.Float64s(flights)
		tr.Flight = flights[len(flights)/2]
	} else {
		tr.Flight = max(tr.End/50, 1)
	}
	tr.End += tr.Flight // room for the last packets to land
	return tr, nil
}
