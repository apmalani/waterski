package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var speeds = []float64{1.0 / 16, 1.0 / 8, 1.0 / 4, 1.0 / 2, 1, 2, 4, 8, 16, 32, 64}

const speed1x = 4

type model struct {
	tr      *Trace
	pf      Profile
	now     float64
	rate    float64 // trace time per real second at 1x
	speed   int
	playing bool
	flow    int // 0 shows every flow, otherwise tr.Flows[flow-1]
	sel     int // selected line among the visible log entries; -1 follows the newest
	w, h    int
	last    time.Time
}

func newModel(tr *Trace, pf Profile) model {
	// at 1x a typical packet takes about a second to cross
	return model{tr: tr, pf: pf, rate: tr.Flight, speed: speed1x, playing: true, sel: -1, w: 120, h: 40}
}

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second/30, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd { return tick() }

func (m model) flowName() string {
	if m.flow == 0 {
		return ""
	}
	return m.tr.Flows[m.flow-1]
}

// visible is the log so far, filtered to the selected flow.
func (m model) visible() []Entry {
	f := m.flowName()
	var out []Entry
	for _, e := range m.tr.Entries {
		if e.T > m.now {
			break
		}
		if f == "" || e.flow() == f {
			out = append(out, e)
		}
	}
	return out
}

// step moves to the time of the next (dir 1) or previous (dir -1) log entry in the selected flow.
func (m *model) step(dir int) {
	f := m.flowName()
	best := -1.0
	for _, e := range m.tr.Entries {
		if f != "" && e.flow() != f {
			continue
		}
		if dir > 0 && e.T > m.now {
			best = e.T
			break
		}
		if dir < 0 && e.T < m.now {
			best = e.T
		}
	}
	if best >= 0 {
		m.now = best
	} else if dir < 0 {
		m.now = 0
	}
	m.playing, m.sel = false, -1
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tickMsg:
		t := time.Time(msg)
		if m.playing && !m.last.IsZero() {
			m.now += t.Sub(m.last).Seconds() * m.rate * speeds[m.speed]
			if m.now >= m.tr.End {
				m.now, m.playing = m.tr.End, false
			}
		}
		m.last = t
		return m, tick()
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case " ", "p":
			if m.now >= m.tr.End {
				m.now = 0
			}
			m.playing = !m.playing
		case "right", "l":
			m.step(1)
		case "left", "h":
			m.step(-1)
		case "shift+right", "L":
			m.now = math.Min(m.now+m.tr.End/20, m.tr.End)
		case "shift+left", "H":
			m.now = math.Max(m.now-m.tr.End/20, 0)
		case "]", "+", "=":
			m.speed = min(m.speed+1, len(speeds)-1)
		case "[", "-":
			m.speed = max(m.speed-1, 0)
		case "f", "tab":
			m.flow, m.sel = (m.flow+1)%(len(m.tr.Flows)+1), -1
		case "F", "shift+tab":
			m.flow, m.sel = (m.flow+len(m.tr.Flows))%(len(m.tr.Flows)+1), -1
		case "up", "k":
			n := len(m.visible())
			if m.sel < 0 {
				m.sel = n - 1
			}
			m.sel = max(m.sel-1, 0)
		case "down", "j":
			if m.sel >= 0 {
				m.sel++
				if m.sel >= len(m.visible())-1 {
					m.sel = -1
				}
			}
		case "end", "G":
			m.sel = -1
		case "home", "g", "r":
			m.now, m.sel, m.playing = 0, -1, true
		}
	}
	return m, nil
}

var (
	stTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e6edf3"))
	stDim   = lipgloss.NewStyle().Foreground(lipgloss.Color(colDim))
	stDrop  = lipgloss.NewStyle().Foreground(lipgloss.Color(colDrop))
	stSel   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff"))
	stAbout = lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color(colDim))
)

func (m model) View() string {
	w, h := max(m.w, 40), max(m.h, 16)
	detailH, helpH := 5, 1
	bodyH := h - 1 - detailH - helpH
	leftW := max(w*11/20, 30)
	rightW := w - leftW

	diag := frame("network", drawDiagram(m.tr, m.pf, m.now, m.flowName(), leftW-2, bodyH-2), leftW, bodyH)
	vis := m.visible()
	logBody, cur := m.logLines(vis, rightW-2, bodyH-2)
	logPane := frame(fmt.Sprintf("log · %d of %d", len(vis), m.total()), logBody, rightW, bodyH)
	body := lipgloss.JoinHorizontal(lipgloss.Top, diag, logPane)
	details := frame("details", m.details(cur, w-3, detailH-2), w, detailH)
	help := stDim.Render(" space play/pause · ←/→ step · shift+←/→ jump · [/] speed · f flow · ↑/↓ select · G follow · r restart · q quit")
	return strings.Join([]string{m.header(w), body, details, ansi.Truncate(help, w, "")}, "\n")
}

func (m model) total() int {
	f, n := m.flowName(), 0
	for _, e := range m.tr.Entries {
		if f == "" || e.flow() == f {
			n++
		}
	}
	return n
}

func (m model) header(w int) string {
	state := "▶"
	if !m.playing {
		state = "⏸"
	}
	flow := m.flowName()
	if flow == "" {
		flow = "all"
	}
	title := m.tr.Title
	if title == "" {
		title = "trace"
	}
	left := stTitle.Render(" waterski ") + stDim.Render(title)
	right := fmt.Sprintf(" %s %s / %s  %sx  flow: %s ", state, fmtTime(m.now, m.tr.Unit, m.tr.End),
		fmtTime(m.tr.End, m.tr.Unit, m.tr.End), fmtSpeed(speeds[m.speed]), flow)
	barW := w - ansi.StringWidth(left) - ansi.StringWidth(right) - 2
	bar := ""
	if barW > 4 {
		done := int(float64(barW) * m.now / math.Max(m.tr.End, 1e-9))
		done = min(max(done, 0), barW)
		bar = lipgloss.NewStyle().Foreground(lipgloss.Color(colFlash)).Render(strings.Repeat("━", done)) +
			stDim.Render(strings.Repeat("─", barW-done))
	}
	return ansi.Truncate(left+" "+bar+" "+right, w, "")
}

// logLines renders the visible entries that fit, and returns the entry the details pane describes.
func (m model) logLines(vis []Entry, w, h int) ([]string, *Entry) {
	if len(vis) == 0 || h <= 0 {
		return nil, nil
	}
	sel := m.sel
	if sel < 0 || sel >= len(vis) {
		sel = len(vis) - 1
	}
	start := max(0, len(vis)-h)
	if m.sel >= 0 {
		start = min(max(0, sel-h/2), max(0, len(vis)-h))
	}
	routeW, typeW, flowW := 0, 0, 0
	for _, nd := range m.tr.Nodes {
		routeW = max(routeW, 2*len([]rune(nd.ID))+1)
	}
	for _, p := range m.tr.Packets {
		typeW = max(typeW, len([]rune(p.Type)))
	}
	for _, f := range m.tr.Flows {
		flowW = max(flowW, len([]rune(f)))
	}
	var out []string
	for i := start; i < len(vis) && i < start+h; i++ {
		e := vis[i]
		mark := " "
		if m.sel >= 0 && i == sel {
			mark = stSel.Render("▶")
		}
		t := fmt.Sprintf("%10s", fmtTime(e.T, m.tr.Unit, m.tr.End))
		var route, typ, text string
		switch e.Kind {
		case 'n':
			route, typ, text = e.N.Node, stDim.Render(pad("note", typeW)), e.N.Text
		default:
			p := e.P
			route = p.Src + "→" + p.Dst
			typ = lipgloss.NewStyle().Foreground(lipgloss.Color(m.pf.style(p.Type).Color)).Bold(true).Render(pad(p.Type, typeW))
			if e.Kind == 'd' {
				text = stDrop.Render("✕ " + p.Reason)
			} else {
				text = m.pf.describe(p)
				if p.Note != "" {
					text += stDrop.Render(" [" + p.Note + "]")
				}
			}
		}
		line := mark + stDim.Render(t) + " " + pad(route, routeW) + " " + typ + " " + stDim.Render(pad(e.flow(), flowW)) + "  " + text
		out = append(out, ansi.Truncate(line, w, "…"))
	}
	return out, &vis[sel]
}

func (m model) details(e *Entry, w, h int) []string {
	if e == nil {
		return []string{stDim.Render("nothing has happened yet: press space to play")}
	}
	var lines []string
	if e.Kind == 'n' {
		lines = append(lines, stTitle.Render(e.N.Node+": ")+e.N.Text)
		return wrapAll(lines, w, h)
	}
	p := e.P
	st := m.pf.style(p.Type)
	head := lipgloss.NewStyle().Foreground(lipgloss.Color(st.Color)).Bold(true).Render(p.Type) +
		stTitle.Render(fmt.Sprintf("  %s → %s  %s", p.Src, p.Dst, p.Flow)) + "  " + m.pf.describe(p)
	lines = append(lines, head)
	when := "sent " + fmtTime(p.Sent, m.tr.Unit, m.tr.End)
	if p.Arrived {
		when += ", arrived " + fmtTime(p.Recv, m.tr.Unit, m.tr.End) + " (in flight " + fmtTime(p.Recv-p.Sent, m.tr.Unit, p.Recv-p.Sent) + ")"
	}
	if p.Dropped {
		when += stDrop.Render(", dropped " + fmtTime(p.Drop, m.tr.Unit, m.tr.End) + ": " + p.Reason)
	}
	if p.Note != "" {
		when += stDrop.Render("  [" + p.Note + "]")
	}
	lines = append(lines, when, stDim.Render(fieldList(p.Fields)))
	if st.About != "" {
		lines = append(lines, stAbout.Render(st.About))
	}
	return wrapAll(lines, w, h)
}

func wrapAll(lines []string, w, h int) []string {
	var out []string
	for _, l := range lines {
		for _, s := range strings.Split(ansi.Wordwrap(l, w, " "), "\n") {
			out = append(out, " "+s)
		}
	}
	if len(out) > h {
		out = out[:h]
	}
	return out
}

// frame draws a rounded box of outer size w×h with a title in the top border.
func frame(title string, body []string, w, h int) string {
	bc := lipgloss.NewStyle().Foreground(lipgloss.Color(colBorder))
	top := "╭─ " + title + " " + strings.Repeat("─", max(0, w-5-ansi.StringWidth(title))) + "╮"
	out := []string{bc.Render(ansi.Truncate(top, w, ""))}
	for i := 0; i < h-2; i++ {
		s := ""
		if i < len(body) {
			s = body[i]
		}
		out = append(out, bc.Render("│")+pad(s, w-2)+bc.Render("│"))
	}
	out = append(out, bc.Render("╰"+strings.Repeat("─", max(0, w-2))+"╯"))
	return strings.Join(out, "\n")
}

// pad fits a styled string to exactly n columns.
func pad(s string, n int) string {
	s = ansi.Truncate(s, n, "")
	return s + strings.Repeat(" ", max(0, n-ansi.StringWidth(s)))
}

var units = []string{"fs", "ps", "ns", "µs", "ms", "s"}

// fmtTime shows t (in the trace's unit) in the unit that suits scale, keeping the trace's resolution:
// 3420 ns at scale 9400 ns is "3.420 µs".
func fmtTime(t float64, unit string, scale float64) string {
	i := 0
	for i < len(units) && units[i] != unit {
		i++
	}
	if i == len(units) {
		return format(t) + " " + unit
	}
	steps := 0
	for i < len(units)-1 && math.Abs(scale) >= 1000 && steps < 1 {
		t, scale = t/1000, scale/1000
		i++
		steps++
	}
	if steps == 0 {
		return format(math.Round(t*100)/100) + " " + units[i]
	}
	return fmt.Sprintf("%.3f %s", t, units[i])
}

func fmtSpeed(s float64) string {
	if s >= 1 {
		return format(s)
	}
	return fmt.Sprintf("1/%d", int(math.Round(1/s)))
}
