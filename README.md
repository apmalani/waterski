# waterski

Replay network simulations in the terminal: a diagram of the nodes with messages moving between
them, next to a detailed log of every message. It reads a JSON-lines trace that anything can
write (FPGA testbenches, network simulators, logs from real hardware), or a waveform (`.vcd`).

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
trace path. A scenario can also be `{"run": "...", "trace": "path"}` to write its own trace, such
as a waveform. In templates, `{key}` is a field's value and `{key?text}` is `text` when the field
is present and not zero. Types without a profile entry get a color and their fields as `key=value`.

## Waveforms

waterski also reads waveforms (`.vcd`), so hardware needs no trace code. It samples each node's
AXI-Stream ports on the clock, joins beats into messages, pairs what was received with what was
sent, and decodes headers with the profile:

```json
{
  "wave": {
    "clock": "top.clk",
    "nodes": [ { "node": "a", "label": "Board A", "scope": "top.a", "addr": "0a000001" } ],
    "tx": { "valid": "m_tvalid", "ready": "m_tready", "data": "m_tdata", "keep": "m_tkeep",
            "last": "m_tlast", "dst": "m_daddr", "fields": { "prio": "m_prio" } },
    "rx": { "valid": "s_tvalid", "data": "s_tdata", "keep": "s_tkeep", "last": "s_tlast",
            "error": "s_tuser", "drop": "rx_drop" },
    "notes": [ { "when": ["done", "!err"], "text": "finished {len} bytes", "count": "done", "flow": "id" } ]
  },
  "profile": {
    "decode": { "type": "11:1", "flow": "20:8&fffffffffffffffe", "flow_label": "RPC" },
    "types": {
      "DATA": { "code": 16, "fields": { "off": "48:4" }, "compute": { "last": "off+bytes-53" } }
    }
  }
}
```

- Signals are relative to each node's `scope`; `tx.dst` is matched against the nodes' `addr` (hex).
  Data is up to 64 bits, byte 0 in the low bits.
- `rx.drop` within 4 cycles of a last beat means the receiver discarded that message (a frame
  error if `rx.error` was set on that beat).
- Fields are `offset:size` or `offset:size&hexmask`, big-endian, plus `bytes`. `compute` adds sums
  (`off+bytes-53`), choices (`S?a:b`, `len=0?a:b`) and templates.
- A note is added on every clock edge where its `when` signals are 1 (`!name`: 0).

Copies with identical bytes are indistinguishable, so "first copy lost, second duplicated" reads
as "both arrived"; a different message of the same length counts as a corrupted copy. Messages
lost or still in flight when the recording ends show as "never arrived".
