# Multi-sensor synchronization design

Status: design only. No multi-sensor coordinator, camera producer protocol, aggregate stream, or
mmwcore reader described here is implemented yet. This design does not change
`mmwcli.capture_stream.v1`.

## Boundaries

`mmwcli` is the acquisition coordinator. It owns the radar lifecycle, the finite session
transaction, cancellation, bounded buffering, and controlled external producer processes. The
first camera integration is a strict external producer contract over caller-selected inherited
handles. The repository does not embed a camera SDK, enumerate cameras, add a camera-specific CGo
adapter, or infer a device clock.

`mmwcore` remains an offline processing library. It may decode a published session or a
caller-owned `BinaryIO`, but it does not launch producers, open devices or sockets, close the
caller's stream, or resume an incomplete session.

## Session and source contracts

The proposed aggregate schemas are `mmwcli.multisensor_session.v1` for the published directory and
`mmwcli.multisensor_stream.v1` for provisional delivery. One random session ID binds both. Every
source has a unique stable `source_id`, a kind such as `radar` or `camera`, a producer name and
version, a finite item and byte limit, a payload contract, and exactly one clock contract.

The coordinator stages this shape under `OUT.part`:

```text
OUT.part/
  session.json
  sensors/
    radar-0/
      source.json
      adc.bin
      index.bin
      radar.cfg
      capture.json
    camera-0/
      source.json
      frames.bin
      index.bin
```

Names are fixed leaves or validated source IDs; traversal, links, devices, unknown `.part` files,
and sparse payloads are rejected. `session.json` records every required leaf's size and SHA-256,
the source outcomes, clock mappings, synchronization grade, and the aggregate counts. A source may
declare a different fixed payload filename, but it cannot add undeclared leaves. Board geometry and
camera calibration are explicit metadata, never inferred from a radar family or image dimensions.

### Fixed little-endian index

Each `index.bin` uses schema `mmwcli.sensor_index.v1`. Its 32-byte header is:

| Offset | Size | Field |
| ---: | ---: | --- |
| 0 | 8 | ASCII `MMWSIDX1` |
| 8 | 2 | major version, exactly 1 |
| 10 | 2 | header bytes, exactly 32 |
| 12 | 2 | entry bytes, exactly 64 |
| 14 | 2 | flags, zero in v1 |
| 16 | 8 | item count |
| 24 | 8 | payload byte count |

Every 64-byte entry is unsigned little-endian:

| Offset | Size | Field |
| ---: | ---: | --- |
| 0 | 8 | zero-based item index |
| 8 | 8 | payload byte offset |
| 16 | 8 | payload byte size |
| 24 | 8 | raw source-clock ticks |
| 32 | 8 | wrap count |
| 40 | 8 | sample/exposure duration in ticks; zero means unavailable |
| 48 | 8 | `sync_event_id`; `MAX_U64` means no associated event |
| 56 | 4 | flags, zero in v1 |
| 60 | 4 | reserved, zero |

Indices and payload ranges must be contiguous, ordered, start at zero, remain within declared
bounds, and exactly cover the payload file. Unknown flags fail. Static source metadata defines what
the tick denotes, for example radar frame start or camera exposure midpoint. Event IDs are compared
across sources, never inferred from item indices. A source declares its allowed item cardinality per
event; validation rejects an absent required event, a missing or unexpected duplicate association,
and cardinality outside that declaration. Valid event IDs exclude `MAX_U64`, never wrap, and session
creation aborts rather than overflowing the ID space.

## Clock model

A source clock declares:

- `clock_id`: an opaque identifier unique to one clock domain in the session;
- `tick_hz`: positive integer ticks per second;
- `wrap_ticks`: zero when ticks do not wrap during the session, otherwise the exclusive modulus;
- `timestamp_semantics`: the physical event represented by an index tick.

When `wrap_ticks` is nonzero, `ticks < wrap_ticks` and the index carries the explicit wrap count.
The checked unwrapped value is `wrap_count * wrap_ticks + ticks`; it must be monotonic and fit
unsigned 64-bit. When it is zero, `wrap_count` must also be zero. The coordinator never guesses a
wrap from arrival order.

The coordinator's host monotonic clock has its own `clock_id` and nanosecond rate. It is meaningful
only for the recorded host and boot. A clock observation contains one source tick plus
`host_before_ns` and `host_after_ns`, sampled immediately before and after the observation. The true
host time is an interval, not their midpoint by assertion, and `before <= after` is required.

Source time is mapped to host monotonic time by non-overlapping affine segments. Each segment
declares a half-open unwrapped-tick range, source and host origins, a positive rational scale
`scale_num / scale_den` in host nanoseconds per source tick, the supporting observation IDs, and a
nonnegative `uncertainty_ns`. Consumers do not extrapolate outside a segment. The reported interval
uses floor for its lower bound and ceiling for its upper bound; uncertainty includes observation
width, fit residual, drift allowance, trigger latency, and quantization.

DCA1000 packet arrival time and camera pipe arrival time are transport observations, never radar
sample or camera exposure time. Software-triggered radar frame times are derived from the bounded
host trigger interval and declared frame period, so their uncertainty cannot be smaller than that
start interval.

## Synchronization grades

- `software_barrier`: sources are armed behind a coordinator barrier and receive bounded host
  start intervals. It provides alignment with measured uncertainty, not simultaneous sampling.
- `external_trigger`: required sources bind each triggered item to the same recorded physical event
  through `sync_event_id`; `MAX_U64` is invalid where an event is required. Required-source event
  sets and declared per-event cardinalities must agree, without assuming one item per event or
  matching item indices. Trigger routing and sensor response uncertainty remain explicit.
- `ptp`: a source clock has recorded PTP domain/grandmaster evidence and a bounded mapping to the
  aggregate clock. Merely using Ethernet or wall-clock time does not qualify.

Grades are evidence labels, not substitutes for `uncertainty_ns`. A session uses the weakest grade
among required sources, while each source retains its own evidence and uncertainty.

## Provisional and transactional state

A controlled producer emits one bounded SESSION, zero or more ordered ITEM records, one END, and
then EOF. ITEM data is provisional. END binds source ID, counts, payload and index sizes, and
SHA-256 values, but it is not a commit. Missing END, missing EOF, trailing bytes, a clock/index
violation, or producer exit disagreement aborts the source.

Only after every required source has reached END+EOF, all hardware/process cleanup has succeeded,
all indices and hashes have been revalidated, and the complete directory has been published without
overwrite may the coordinator emit the single global COMMIT followed by EOF. COMMIT is constructed
from the already-published directory evidence and carries the session ID and `session.json` digest.
If COMMIT delivery fails, the directory remains valid while the live aggregate stream is rejected.
Failure attempts one global ABORT followed by EOF; truncation or missing EOF is also an abort.

Each source queue, item size, item count, payload total, reorder window, and aggregate memory total
has a configured hard bound. The coordinator writes accepted bytes to the staged artifact before
making an item provisionally visible. It never drops, fills, duplicates, silently spills, or grows
an unbounded queue. Backpressure or a disconnected consumer cancels every source and enters bounded
cleanup.

## Delivery batches

Implementation is intentionally split into independently testable work:

A. Freeze the JSON schemas, fixed index codec, limits, and cross-language golden fixtures.
B. Implement clock validation, wrap arithmetic, affine segments, uncertainty propagation, and
   synchronization-grade evidence with deterministic vectors.
C. Implement the coordinator state machine, bounded queues, cancellation, staged directory, and
   published-evidence COMMIT/EOF transaction using fake sources.
D. Adapt the existing finite radar capture/session output without changing capture-stream v1.
E. Add the controlled external producer contract and fake camera producer; vendor camera SDKs stay
   outside this repository.
F. Add mmwcore's read-only published-session and caller-owned `BinaryIO` decoders, then run
   cross-language corruption, backpressure, clock-wrap, and terminal-state tests.

No batch may claim hardware synchronization from offline tests. External-trigger and PTP support
remain experimental until their evidence records can be reproduced.
