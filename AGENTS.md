# mmwcli

This file records only stable repository contracts. Keep one-off task decisions out of it.

## Skills

Use a matching `~/.codex/skills` skill only when it helps the current task. Common choices are
`simplify` (`code-simplifier`), `grill-me`, `code-review` (when installed), and `prototype`; these
are examples, not an allowlist. Read the selected `SKILL.md` first, and never run skills
mechanically or turn one-off outputs into permanent constraints.

## Role

- Own IWR6843 ES2 + DCA1000 acquisition on Windows/amd64.
- Publish finite capture as `mmwcli.take.v3` with an immutable `mmwcli.snapshot.v1`, or stream
  complete ADC frames to OpenMMW.
- Keep camera capture optional so radar-only capture remains available.
- Leave DSP, tracking, and benchmarks to mmwcore; leave datasets, models, inference, and Web to
  OpenMMW.

## Preserve

- Require explicit COM and D2XX selection; validate inputs before opening hardware or output.
- Preserve ADC bytes exactly. mmwcli does not process or repair them.
- Own the raw `TAKE.capture.part -> TAKE.capture` transaction; callers never add `.part`.
- Stream complete frames only. Stdout is machine data; logs use stderr.

## Checks

Run only checks affected by the change. The full gate is `.github/workflows/ci.yml`.

```text
gofmt -l cmd internal
go test -count=1 ./...
go vet ./...
go build -trimpath ./cmd/mmwcli
```

Hardware validation is a separate, explicitly requested step.
