# waterski

Replay network simulations in the terminal: a diagram of the nodes with messages moving between
them, next to a detailed log of every message. It works with anything that can write a trace:
FPGA testbenches, network simulators, or logs from real hardware.

```
go install github.com/apmalani/waterski@latest
```

## Use

```
waterski run <scenario>     run a scenario from waterski.json, then replay its trace
waterski run -log <name>    run it and print the log instead
waterski view [trace]       replay a trace
waterski log [trace]        print the detailed log
waterski list               list the scenarios
```

Keys: `space` play/pause, `←/→` step between messages, `shift+←/→` jump, `[`/`]` speed,
`f` show one flow, `↑/↓` select a log line, `G` follow, `r` restart, `q` quit.

## Trace format

One JSON object per line. Times are numbers in `time_unit` (default `ns`).

```json
{"ev":"meta","title":"ping test","time_unit":"ns"}
{"ev":"node","node":"a","label":"Board A"}
{"ev":"send","t":10,"id":1,"src":"a","dst":"b","type":"REQ","flow":"txn 7","fields":{"len":64}}
{"ev":"recv","t":30,"id":1}
{"ev":"drop","t":40,"id":2,"reason":"lost"}
{"ev":"note","t":31,"node":"b","flow":"txn 7","count":"served","text":"handled the request"}
```

- `send` starts a message; `id` is unique. `recv` and `drop` refer to it. A message with
  neither is drawn as never arriving. Optional `note` text is shown with it, e.g. "duplicate".
- `flow` groups related messages (a transaction, connection, or RPC); press `f` to show one.
- `note` records something that happened at a node. `count` adds a counter to its box.
- Nodes appear on first use; `node` events give them labels and, optionally, `x`/`y` in 0..1.

## waterski.json

```json
{
  "trace": "build/trace.jsonl",
  "scenarios": { "basic": "make sim TRACE={trace}" },
  "profile": {
    "types": {
      "REQ": { "color": "#4da3ff", "short": "REQ {len}", "log": "asks for {len} bytes{retry? (retry)}",
               "about": "What a REQ is for." }
    }
  }
}
```

Scenarios are shell commands run from the current directory, with `{trace}` replaced by the
trace path. In templates, `{key}` is a field's value and `{key?text}` is `text` when the field
is present and not zero. Types without a profile entry get a color and their fields as `key=value`.
