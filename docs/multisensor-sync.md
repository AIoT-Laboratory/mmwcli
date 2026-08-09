# Multi-sensor synchronization

Status: implemented for finite software-barrier radar-plus-camera capture. mmwcli provides the
coordinator, transactional directory, fixed index, controlled producer protocol, fixed-frame
camera adapter, and aggregate live stream. mmwcore provides caller-owned offline and live readers.
External-trigger and PTP grades remain planned. This contract does not change
`mmwcli.capture_stream.v1`.

## Boundaries

`mmwcli` is the acquisition coordinator. It owns the radar lifecycle, the finite session
transaction, cancellation, bounded buffering, and controlled external producer processes. The
built-in camera integration is a strict external producer contract over caller-selected inherited
handles. The repository does not embed a camera SDK, enumerate cameras, add a camera-specific CGo
adapter, or infer a device clock.

`mmwcore` remains a processing library over caller-owned data. It decodes published sessions and
caller-owned live `BinaryIO` streams, but it does not launch producers, open devices or sockets,
close the caller's stream, or resume an incomplete session.

### Migration and minimum producer interface

The old OpenMMW arrangement built around `mmwcore.session`, UDP handoff, and processes watching a
shared capture directory is a migration source, not a compatibility target. Device and process
lifecycle moves to `mmwcli`; completed-directory and caller-owned-stream consumption moves to
`mmwcore.io`. Producers never coordinate through partially written shared files, and this
implementation does not restore a live session API in mmwcore.

`mmwcli` is the only global acquisition coordinator and capture-session publisher. Each external
sensor producer has separate bounded control and data handles. Its minimum control sequence is
`READY -> ARM -> START -> STOP` for success or `READY/ARM/START -> CANCEL` on failure. Every control
message carries the session and source IDs plus a strictly increasing per-source sequence; the
producer must return the matching ACK before the coordinator sends the next step. READY confirms
the contract, ARM opens the finite device/buffers without sampling, START releases acquisition,
and STOP or CANCEL performs bounded cleanup. Every step has a coordinator-owned deadline; a late,
missing, duplicate, or mismatched ACK cancels the global acquisition.

The data handle independently emits exactly `SESSION -> ITEM* -> END -> EOF`. SESSION fixes the
source, limits, clock, payload, and metadata contract before ITEM; ITEM is provisional; END binds
the finite result; EOF proves closure. Control ACKs never substitute for data END+EOF, and a valid
data terminal never substitutes for STOP/CANCEL cleanup.

## Session and source contracts

The aggregate schemas are `mmwcli.multisensor_session.v1` for the published directory and
`mmwcli.multisensor_stream.v1` for provisional delivery. One random session ID binds both. Every
source has a unique stable `source_id`, a kind such as `radar` or `camera`, a producer name and
version, a finite item and byte limit, a payload contract, exactly one clock contract, and an
immutable `required` boolean declared before acquisition starts.

The coordinator stages this shape under `OUT.part`:

```text
OUT.part/
  session.json
  sensors/
    radar-0/
      adc.bin
      index.bin
      radar.cfg
      capture.json
    camera-0/
      frames.bin
      index.bin
```

Names are fixed leaves or validated source IDs; traversal, links, devices, unknown `.part` files,
and sparse payloads are rejected. Source contracts are entries inside the single root
`session.json`; there is no per-source `source.json`. That root records every required leaf's size
and SHA-256, the source outcomes, clock mappings, synchronization grade, and aggregate counts. A
camera source may declare a different payload filename such as `camera.mjpeg`, but it cannot add
undeclared leaves. Board geometry and camera calibration are explicit metadata, never inferred from
a radar family or image dimensions.

Application-specific metadata uses one `application_metadata` JSON object with namespaced
top-level keys (for example `org.openmmw.training`), never flat additions to protocol objects. Its
exact encoded bytes participate in the containing record digest, the source END lineage, the
published `session.json` digest, and therefore the global COMMIT. A digest proves integrity and
lineage, not identity or authenticity; this local producer contract adds no authentication layer.

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

### Physical synchronization event ledger

`session.json` owns one closed `sync_events` ledger. Each entry identifies one physical event with
a unique `sync_event_id`, the event clock's `clock_id`, raw event tick and wrap count, a closed edge
value (`rising` or `falling`), a closed evidence kind plus generator/observer and routing identity,
the supporting observation IDs, and a nonnegative `uncertainty_ns`. The event clock and tick obey
the same wrap and affine-mapping rules as source index ticks.

The coordinator creates a ledger entry only from accepted trigger-generation or
hardware-observation evidence. A source `index.bin` may only reference an existing ledger ID;
equal item indices,
producer-local counters, arrival order, or equal self-assigned numbers do not establish a shared
physical event. Ledger IDs are session-local, immutable after first reference, and every reference
must agree with the source's declared per-event cardinality.

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

Source time is mapped to host monotonic time by ordered affine segments. Each segment declares a
nonempty half-open unwrapped-tick range, source and host origins, a positive rational scale
`scale_num / scale_den` in host nanoseconds per source tick, the supporting observation IDs, and a
nonnegative `uncertainty_ns`. Segment ranges may meet but never overlap; checked arithmetic must fit
the declared integer domains, and nominal mapped time must not move backwards at a segment boundary.
Every index tick of a completed source and every referenced event-ledger tick must be covered by
exactly one segment. Gaps, duplicate coverage, or a tick outside all segments invalidate the source;
consumers never extrapolate. The reported interval uses floor for its lower bound and ceiling for
its upper bound; uncertainty includes observation width, fit residual, drift allowance, trigger
latency, and quantization.

DCA1000 packet arrival time and camera pipe arrival time are transport observations, never radar
sample or camera exposure time. Software-triggered radar frame times are derived from the bounded
host trigger interval and declared frame period, so their uncertainty cannot be smaller than that
start interval.

An ordinary camera may use `delivery_observed`. Its producer sends zero clock fields and mmwcli
assigns a host-relative nanosecond tick only after the complete ITEM arrives. This is a delivery
time, never an exposure claim. The aggregate stream's RADAR_START record maps radar tick zero to a
conservative host interval, so radar and delivery-observed camera items can be paired during live
inference. A real device exposure clock remains `exposure_midpoint` with an explicit affine map.
Matching uses each item's mapped interval, including duration and uncertainty. For configured
causal lag `[lag_min, lag_max]`, item B may match item A only when the possible lag interval
`[B.start - A.end, B.end - A.start]` intersects that window. Multiple candidates remain ambiguous
unless an event ID or application policy resolves them; arrival order and nearest delivery time do
not.

## Synchronization grades

- `software_barrier`: sources are armed behind a coordinator barrier and receive bounded host
  start intervals. It provides alignment with measured uncertainty, not simultaneous sampling.
- `external_trigger`: required sources bind each triggered item to the same physical-event ledger
  entry through `sync_event_id`; `MAX_U64` is invalid where an event is required. Required-source
  event sets and declared per-event cardinalities must agree, without assuming one item per event
  or matching item indices. Trigger routing and sensor response uncertainty remain explicit.
- `ptp`: a source clock has recorded PTP domain/grandmaster evidence and a bounded mapping to the
  aggregate clock. Merely using Ethernet or wall-clock time does not qualify.

Grades are evidence labels, not substitutes for `uncertainty_ns`. A session uses the weakest grade
among required sources, while each source retains its own evidence and uncertainty.

## Provisional and transactional state

A controlled producer emits one bounded SESSION, zero or more ordered ITEM records, one END, and
then EOF. ITEM data is provisional. END binds source ID, counts, payload and index sizes, and
SHA-256 values, but it is not a commit. Missing END, missing EOF, trailing bytes, a clock/index
violation, or producer exit disagreement aborts the source.

A source's declared `required` value cannot be downgraded after start. A required source may publish
only with outcome `complete`, which requires END+EOF and successful validation. An optional source
has exactly one terminal outcome: `complete`, `failed`, or `omitted`. `failed` and `omitted` sources
contribute no payload/index leaves to the published directory; any staged partial leaves are
rejected before publication. All declared sources must reach one terminal outcome and all producer/device
cleanup must finish before the aggregate can be published. A source-level `complete` remains
provisional and is never a substitute for the global COMMIT.

Only after every required source is `complete`, every optional source is terminal, all cleanup has
succeeded, all indices, event references, clock mappings, and hashes have been revalidated, and the
complete directory has been published without overwrite may the coordinator emit the single global
COMMIT followed by EOF. The COMMIT is constructed from the already-published directory evidence
and carries the session ID and `session.json` digest.
If COMMIT delivery fails, the directory remains valid while the live aggregate stream is rejected.
Any required-source failure, terminal disagreement, validation/cleanup failure, or publication
failure forbids COMMIT and attempts one global ABORT followed by EOF; truncation or missing EOF is
also an abort. An allowed optional-source failure is recorded in `session.json` but does not change
a successfully published aggregate into an abort.

Each source queue, item size, item count, payload total, reorder window, and aggregate memory total
has a configured hard bound. The coordinator writes accepted bytes to the staged artifact before
making an item provisionally visible. It never drops, fills, duplicates, silently spills, or grows
an unbounded queue. Backpressure or a disconnected consumer cancels every source and enters bounded
cleanup.

Offline training joins use the fixed key `(session_id, source_id, item_index)` and append
`sync_event_id` when event identity is required; filenames, row order, and timestamps are not keys.
A real-time inference consumer may compute from provisional ITEM records, but its results remain
provisional. It may publish no derived artifact until it has validated the single global COMMIT and
following EOF; ABORT, truncation, or missing EOF discards or explicitly quarantines those results.

## User workflow

`mmwcli multisensor init` creates a single-camera `delivery_observed` plan around a caller-supplied
command. Non-JPEG formats require `--frame-bytes`; exact `image.jpeg.v1` instead requires
`--max-item-bytes` and selects the marker-aware MJPEG adapter. `mmwcli multisensor check` validates
and prints either plan without starting a process or hardware. Either radar capture command accepts
`--multisensor-plan PLAN`; adding `--stream` emits the aggregate stream on binary stdout. The
generated adapters work with ffmpeg, GStreamer, or a vendor CLI that writes exact-size raw frames or
concatenated complete JPEG images.

mmwcore opens published training data with `open_multisensor_capture`, opens the nested radar
capture through `source.open_radar_capture`, and pairs conservative intervals with `causal_pairs`.
`open_multisensor_stream` yields provisional live items; radar and delivery-observed camera items
have mapped host intervals. Derived results remain provisional until COMMIT and EOF, and
`commit.accepts(item)` excludes failed or omitted optional sources.

Offline tests do not claim hardware synchronization. External-trigger and PTP support remain
planned until their evidence paths and hardware records are implemented.
