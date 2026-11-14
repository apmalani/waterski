// waterski replays network simulation traces in the terminal: a diagram of the nodes with
// messages moving between them, next to a detailed log of every message.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const usage = `waterski: replay network simulations in the terminal

usage:
  waterski run [-log] <scenario>   run a scenario from waterski.json, then replay its trace
  waterski view [trace]            replay a trace (default: "trace" in waterski.json)
  waterski log [trace]             print the detailed log
  waterski list                    list the scenarios

every command takes -config <file> (default waterski.json); view also takes
-frame <time> -size <cols>x<rows> to print a single frame instead of playing`

// Config is waterski.json: how to run a project's simulations and how to describe its messages.
type Config struct {
	Trace     string            `json:"trace"`     // where scenarios write their trace
	Scenarios map[string]string `json:"scenarios"` // shell commands; {trace} is replaced by the trace path
	Profile   Profile           `json:"profile"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "waterski:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Println(usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	fl := flag.NewFlagSet(cmd, flag.ContinueOnError)
	cfgPath := fl.String("config", "waterski.json", "config file")
	asLog := fl.Bool("log", false, "print the log instead of playing (run)")
	at := fl.Float64("frame", -1, "print the frame at this time, in the trace's unit (view)")
	size := fl.String("size", "120x40", "frame size (view -frame)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*cfgPath, fl)
	if err != nil {
		return err
	}
	tracePath := cfg.Trace
	if tracePath == "" {
		tracePath = "waterski.trace.jsonl"
	}

	switch cmd {
	case "list":
		for _, name := range scenarioNames(cfg) {
			fmt.Printf("%-12s %s\n", name, cfg.Scenarios[name])
		}
		return nil
	case "run":
		if fl.NArg() != 1 {
			return fmt.Errorf("run needs one scenario: %s", strings.Join(scenarioNames(cfg), ", "))
		}
		if err := runScenario(cfg, fl.Arg(0), tracePath); err != nil {
			return err
		}
		if *asLog {
			return printLog(tracePath, cfg.Profile)
		}
		return play(tracePath, cfg.Profile)
	case "view", "log":
		if fl.NArg() > 0 {
			tracePath = fl.Arg(0)
		}
		if cmd == "log" {
			return printLog(tracePath, cfg.Profile)
		}
		if *at >= 0 {
			return printFrame(tracePath, cfg.Profile, *at, *size)
		}
		return play(tracePath, cfg.Profile)
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
}

// loadConfig reads the config file; a missing default file just means no scenarios and no profile.
func loadConfig(path string, fl *flag.FlagSet) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		explicit := false
		fl.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "config" })
		if !explicit {
			return cfg, nil
		}
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %v", path, err)
	}
	return cfg, nil
}

func scenarioNames(cfg Config) []string {
	names := make([]string, 0, len(cfg.Scenarios))
	for n := range cfg.Scenarios {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func runScenario(cfg Config, name, tracePath string) error {
	script, ok := cfg.Scenarios[name]
	if !ok {
		return fmt.Errorf("no scenario %q (have: %s)", name, strings.Join(scenarioNames(cfg), ", "))
	}
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o755); err != nil {
		return err
	}
	if err := os.Remove(tracePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	quoted := "'" + strings.ReplaceAll(tracePath, "'", `'\''`) + "'"
	c := exec.Command("sh", "-c", strings.ReplaceAll(script, "{trace}", quoted))
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	fmt.Fprintf(os.Stderr, "waterski: running %s\n", name)
	if err := c.Run(); err != nil {
		return fmt.Errorf("scenario %s failed: %v", name, err)
	}
	return nil
}

func load(path string) (*Trace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tr, err := parseTrace(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return tr, nil
}

func play(path string, pf Profile) error {
	tr, err := load(path)
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(newModel(tr, pf), tea.WithAltScreen()).Run()
	return err
}

func printFrame(path string, pf Profile, at float64, size string) error {
	tr, err := load(path)
	if err != nil {
		return err
	}
	m := newModel(tr, pf)
	if _, err := fmt.Sscanf(size, "%dx%d", &m.w, &m.h); err != nil {
		return fmt.Errorf("bad -size %q: want <cols>x<rows>", size)
	}
	m.now, m.playing = at, false
	fmt.Println(m.View())
	return nil
}

func printLog(path string, pf Profile) error {
	tr, err := load(path)
	if err != nil {
		return err
	}
	for _, e := range tr.Entries {
		t := fmtTime(e.T, tr.Unit, tr.End)
		switch e.Kind {
		case 'n':
			fmt.Printf("%10s  %-9s %-8s %-8s %s\n", t, e.N.Node, "note", e.N.Flow, e.N.Text)
		case 'd':
			fmt.Printf("%10s  %-9s %-8s %-8s ✕ %s\n", t, e.P.Src+"→"+e.P.Dst, e.P.Type, e.P.Flow, e.P.Reason)
		default:
			text := pf.describe(e.P)
			if e.P.Note != "" {
				text += " [" + e.P.Note + "]"
			}
			fmt.Printf("%10s  %-9s %-8s %-8s %s\n", t, e.P.Src+"→"+e.P.Dst, e.P.Type, e.P.Flow, text)
		}
	}
	return nil
}
