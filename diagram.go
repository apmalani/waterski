package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type cell struct {
	r     rune
	color string
	bold  bool
}

// canvas is a grid of colored characters, rendered row by row.
type canvas struct {
	w, h  int
	cells []cell
}

func newCanvas(w, h int) *canvas {
	c := &canvas{w: max(w, 0), h: max(h, 0)}
	c.cells = make([]cell, c.w*c.h)
	for i := range c.cells {
		c.cells[i].r = ' '
	}
	return c
}

func (c *canvas) set(x, y int, r rune, color string, bold bool) {
	if x >= 0 && y >= 0 && x < c.w && y < c.h {
		c.cells[y*c.w+x] = cell{r, color, bold}
	}
}

func (c *canvas) text(x, y int, s, color string, bold bool) {
	for _, r := range s {
		c.set(x, y, r, color, bold)
		x++
	}
}

func (c *canvas) lines() []string {
	out := make([]string, c.h)
	for y := range out {
		row := c.cells[y*c.w : (y+1)*c.w]
		var b strings.Builder
		for i := 0; i < len(row); {
			j := i
			var seg strings.Builder
			for j < len(row) && row[j].color == row[i].color && row[j].bold == row[i].bold {
				seg.WriteRune(row[j].r)
				j++
			}
			st := lipgloss.NewStyle().Bold(row[i].bold)
			if row[i].color != "" {
				st = st.Foreground(lipgloss.Color(row[i].color))
			}
			b.WriteString(st.Render(seg.String()))
			i = j
		}
		out[y] = b.String()
	}
	return out
}

const (
	colLink   = "#3a4655"
	colBorder = "#5c6b7e"
	colFlash  = "#3ddc84"
	colDim    = "#8b95a3"
	colDrop   = "#ff5c5c"
)

type box struct {
	x, y, w, h int     // top-left corner and size, in cells
	cx, cy     float64 // center
}

// counters returns, per node, each counter's name and its value at time now.
// Names come from the whole trace so boxes keep their size while playing.
func counters(tr *Trace, now float64, flow string) map[string][][2]string {
	names := map[string][]string{}
	vals := map[string]map[string]int{}
	for _, n := range tr.Notes {
		if n.Count == "" {
			continue
		}
		if vals[n.Node] == nil {
			vals[n.Node] = map[string]int{}
		}
		if _, ok := vals[n.Node][n.Count]; !ok {
			names[n.Node] = append(names[n.Node], n.Count)
			vals[n.Node][n.Count] = 0
		}
		if n.T <= now && (flow == "" || n.Flow == flow) {
			vals[n.Node][n.Count]++
		}
	}
	out := map[string][][2]string{}
	for node, list := range names {
		for _, name := range list {
			out[node] = append(out[node], [2]string{name, fmt.Sprint(vals[node][name])})
		}
	}
	return out
}

func layoutBoxes(tr *Trace, cnt map[string][][2]string, w, h int) []box {
	n := len(tr.Nodes)
	boxes := make([]box, n)
	maxW, maxH := 0, 0
	capW := w // two boxes may share a row, so each gets at most half the width
	if n >= 2 {
		capW = max((w-3)/2, 8)
	}
	for i, nd := range tr.Nodes {
		bw := len([]rune(nd.title())) + 4
		for _, c := range cnt[nd.ID] {
			bw = max(bw, len([]rune(c[0]))+len(c[1])+5)
		}
		bw = min(bw, capW)
		boxes[i].w, boxes[i].h = bw, 3+len(cnt[nd.ID])
		maxW, maxH = max(maxW, bw), max(maxH, boxes[i].h)
	}
	// unplaced nodes go around an ellipse, scaled to fill the area
	fx, fy := make([]float64, n), make([]float64, n)
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for i, nd := range tr.Nodes {
		switch {
		case nd.Placed:
			fx[i], fy[i] = nd.X, nd.Y
			continue
		case n == 1:
			fx[i], fy[i] = 0.5, 0.5
		case n == 2:
			fx[i], fy[i] = float64(i), 0.5
		default:
			a := math.Pi/2 + 2*math.Pi*float64(i+1)/float64(n)
			fx[i], fy[i] = math.Cos(a), math.Sin(a)
		}
		minX, maxX = math.Min(minX, fx[i]), math.Max(maxX, fx[i])
		minY, maxY = math.Min(minY, fy[i]), math.Max(maxY, fy[i])
	}
	for i, nd := range tr.Nodes {
		if !nd.Placed {
			if maxX > minX {
				fx[i] = (fx[i] - minX) / (maxX - minX)
			}
			if maxY > minY {
				fy[i] = (fy[i] - minY) / (maxY - minY)
			}
		}
		b := &boxes[i]
		b.cx = float64(maxW)/2 + 1 + fx[i]*math.Max(float64(w-maxW-2), 0)
		b.cy = float64(maxH)/2 + fy[i]*math.Max(float64(h-maxH), 0)
		b.x, b.y = int(math.Round(b.cx-float64(b.w)/2)), int(math.Round(b.cy-float64(b.h)/2))
	}
	return boxes
}

// drawDiagram draws the nodes, the links between them and every packet in flight at time now.
func drawDiagram(tr *Trace, pf Profile, now float64, flow string, w, h int) []string {
	c := newCanvas(w, h)
	cnt := counters(tr, now, flow)
	boxes := layoutBoxes(tr, cnt, w, h)
	idx := map[string]int{}
	for i, nd := range tr.Nodes {
		idx[nd.ID] = i
	}

	linked := map[[2]int]bool{}
	for _, p := range tr.Packets {
		a, b := idx[p.Src], idx[p.Dst]
		if k := [2]int{min(a, b), max(a, b)}; a != b && !linked[k] {
			linked[k] = true
			line(c, boxes[a].cx, boxes[a].cy, boxes[b].cx, boxes[b].cy)
		}
	}

	taken := make([]rect, len(boxes))
	for i, b := range boxes {
		taken[i] = rect{b.x, b.x + b.w, b.y, b.y + b.h - 1}
	}
	nb := len(taken)
	for _, p := range tr.Packets {
		if now < p.Sent || (flow != "" && p.Flow != flow) {
			continue
		}
		land := p.Sent + tr.Flight // never arrived and never dropped
		lost := p.Dropped && !p.Arrived
		if p.Arrived {
			land = p.Recv
		} else if lost {
			land = p.Sent + tr.Flight/2 // lost packets vanish halfway
		}
		gone := land
		if p.Dropped {
			gone = math.Max(land, p.Drop) + tr.Flight*0.75
		}
		if now >= gone {
			continue
		}
		frac := 1.0
		if now < land && land > p.Sent {
			frac = (now - p.Sent) / (land - p.Sent)
		}
		if lost {
			frac /= 2
		}
		x, y := along(boxes, idx[p.Src], idx[p.Dst], frac)
		st := pf.style(p.Type)
		if now >= land && p.Dropped {
			taken = place(c, taken, nb, "✕ "+p.Type, x, y, colDrop, true)
			continue
		}
		if now >= land {
			continue
		}
		taken = place(c, taken, nb, "● "+expand(st.Short, p.Fields), x, y, st.Color, false)
	}

	since := tr.Flight * 0.8
	for i, nd := range tr.Nodes {
		flash := false
		for _, n := range tr.Notes {
			if n.Node == nd.ID && n.T <= now && now-n.T < since && (flow == "" || n.Flow == flow) {
				flash = true
			}
		}
		drawBox(c, boxes[i], nd.title(), cnt[nd.ID], flash)
	}
	return c.lines()
}

type rect struct{ x0, x1, y0, y1 int } // columns [x0, x1), rows [y0, y1]

func (r rect) hits(o rect) bool { return r.x0 < o.x1 && o.x0 < r.x1 && r.y0 <= o.y1 && o.y0 <= r.y1 }

// place draws a label centered near (x, y) without covering a box (the first nb rects) or an
// earlier label, moving it a row or two if needed; on a crowded link it falls back to a dot.
func place(c *canvas, taken []rect, nb int, label string, x, y int, color string, bold bool) []rect {
	n := len([]rune(label))
	for _, dy := range []int{0, 1, -1, 2, -2} {
		r := rect{x - n/2, x - n/2 + n, y + dy, y + dy}
		for _, b := range taken[:nb] {
			if r.hits(b) {
				if x < (b.x0+b.x1)/2 {
					r.x0 = b.x0 - n
				} else {
					r.x0 = b.x1
				}
				r.x1 = r.x0 + n
			}
		}
		free := r.x0 >= 0 && r.x1 <= c.w && r.y0 >= 0 && r.y0 < c.h
		for _, o := range taken {
			free = free && !r.hits(o)
		}
		if free {
			c.text(r.x0, r.y0, label, color, bold)
			return append(taken, r)
		}
	}
	c.set(x, y, []rune(label)[0], color, bold)
	return taken
}

// along returns the cell at fraction frac of the way from box a's edge to box b's edge,
// shifted to one side of the link so the two directions use separate lanes.
func along(boxes []box, a, b int, frac float64) (int, int) {
	A, B := boxes[a], boxes[b]
	dx, dy := B.cx-A.cx, B.cy-A.cy
	exit := func(bx box) float64 {
		s := math.Inf(1)
		if dx != 0 {
			s = math.Min(s, (float64(bx.w)/2+1)/math.Abs(dx))
		}
		if dy != 0 {
			s = math.Min(s, (float64(bx.h)/2+1)/math.Abs(dy))
		}
		return math.Min(s, 0.45)
	}
	ea, eb := exit(A), exit(B)
	s := ea + (1-ea-eb)*frac
	// cells are about twice as tall as wide, so measure the normal in screen space
	lx, ly := dx, 2*dy
	if a > b {
		lx, ly = -lx, -ly
	}
	l := math.Hypot(lx, ly)
	nx, ny := 0.0, 1.0
	if l > 0 {
		nx, ny = -ly/l, lx/l
	}
	side := 1.0
	if a > b {
		side = -1
	}
	return int(math.Round(A.cx + dx*s + 2*nx*side)), int(math.Round(A.cy + dy*s + ny*side))
}

func line(c *canvas, x0, y0, x1, y1 float64) {
	steps := int(math.Max(math.Abs(x1-x0), math.Abs(y1-y0)))
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(max(steps, 1))
		c.set(int(math.Round(x0+(x1-x0)*t)), int(math.Round(y0+(y1-y0)*t)), '·', colLink, false)
	}
}

func drawBox(c *canvas, b box, title string, cnt [][2]string, flash bool) {
	col := colBorder
	if flash {
		col = colFlash
	}
	for y := b.y; y < b.y+b.h; y++ {
		for x := b.x; x < b.x+b.w; x++ {
			c.set(x, y, ' ', "", false)
		}
	}
	for x := b.x + 1; x < b.x+b.w-1; x++ {
		c.set(x, b.y, '─', col, flash)
		c.set(x, b.y+b.h-1, '─', col, flash)
	}
	for y := b.y + 1; y < b.y+b.h-1; y++ {
		c.set(b.x, y, '│', col, flash)
		c.set(b.x+b.w-1, y, '│', col, flash)
	}
	c.set(b.x, b.y, '╭', col, flash)
	c.set(b.x+b.w-1, b.y, '╮', col, flash)
	c.set(b.x, b.y+b.h-1, '╰', col, flash)
	c.set(b.x+b.w-1, b.y+b.h-1, '╯', col, flash)
	title = cut(title, b.w-2)
	c.text(b.x+(b.w-len([]rune(title)))/2, b.y+1, title, "", true)
	for i, kv := range cnt {
		name := cut(kv[0], b.w-5-len(kv[1]))
		pad := b.w - 4 - len([]rune(name)) - len(kv[1])
		c.text(b.x+2, b.y+2+i, name+strings.Repeat(" ", max(pad, 1))+kv[1], colDim, false)
	}
}

// cut shortens s to n runes, marking the cut with an ellipsis.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:max(n, 0)])
	}
	return string(r[:n-1]) + "…"
}
