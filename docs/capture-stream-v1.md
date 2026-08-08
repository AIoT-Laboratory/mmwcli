# Capture stream v1

**mmwcli.capture_stream.v1** is a finite, transport-independent record format for provisional raw
ADC frames. It keeps radar and DCA1000 ownership in mmwcli and gives consumers an integrity-checked
byte contract.

The Go implementation includes the CFG-plan-bound record encoder, bounded `WriterAt` frame mirror,
`StdoutEncoder`, and the optional capture-session mirror/seal hook. mmwcore implements the matching
`mmwcore.io.CaptureStreamReader` decoder over a caller-owned `BinaryIO`. Both production capture
routes expose the same optional stream:

~~~text
mmwcli studio-cli capture CFG OUTDIR --port PORT --stream [options]
mmwcli debug-cli capture CFG OUTDIR --enhanced-port PORT ... --stream [options]
~~~

`OUTDIR` is always the authoritative strict **mmwcli.capture_session.v1** transaction, whether or
not `--stream` is present. The flag adds a provisional stdout mirror; it does not create a second
capture mode or a stream-only route.

## Trust boundary

With `--stream`, stdout must be a dedicated OS file or pipe and contains capture-stream bytes from
its first byte through EOF; plans, progress, statistics, and errors go to stderr. `StdoutEncoder`
owns and closes that stdout handle, including to interrupt a blocked write on Windows and Linux.
Record SHA-256 values provide integrity checking, not peer identity or authentication.

## Record framing

Every record starts with one 80-byte header. All integers are unsigned little-endian values.

| Offset | Size | Field |
| ---: | ---: | --- |
| 0 | 8 | ASCII magic MMWSTRM1 |
| 8 | 2 | protocol major, exactly 1 |
| 10 | 2 | header size, exactly 80 |
| 12 | 2 | record type |
| 14 | 2 | flags, zero in v1 |
| 16 | 8 | record sequence |
| 24 | 8 | item index |
| 32 | 8 | payload byte count |
| 40 | 8 | reserved, zero in v1 |
| 48 | 32 | record SHA-256 |

The digest is:

~~~text
SHA256("mmwcli.capture_stream.record.v1" || NUL || header[0:48] || payload)
~~~

Consumers must apply the type-specific payload bound before allocating or reading the payload, then
verify the digest. A malformed, truncated, unsupported, out-of-sequence, or trailing record aborts
the stream. There is no resynchronization.

Record types are SESSION, RADAR_CONFIG, FRAME, COMMIT, and ABORT, numbered 1 through 5.

## Record order

One valid stream has exactly this state sequence:

~~~text
SESSION(sequence=0, item=0)
RADAR_CONFIG(sequence=1, item=0)
FRAME(sequence=2+i, item=i) for i = 0..frame_count-1
COMMIT or ABORT(sequence=2+frames_emitted, item=frames_emitted)
~~~

Frame indices, logical ADC byte offsets, and record sequences start at zero. Records and frames may
not be skipped, duplicated, replaced, or resumed.

## Session header

SESSION is strict UTF-8 JSON with schema **mmwcli.capture_stream.v1** and a maximum encoded size of
64 KiB. Its exact top-level key set is `schema`, `stream_id`, `producer`, `mode`, `hardware`,
`capture`, `adc`, `radar_config`, and `artifact`. It contains:

- a nonzero 16-byte random stream ID encoded as exactly 32 lowercase hex digits;
- producer name mmwcli and a version of 1..128 valid UTF-8 bytes, with no surrounding whitespace
  or control characters;
- capture mode studio-cli or debug-cli;
- hardware keys `vendor`, `family`, `model`, `revision`, and `identity_source`, with the current
  closed tuple `ti`, `xwr68xx`, empty model, empty revision, and `route_declaration`;
- finite frame count, frame bytes, and their checked expected-byte product;
- explicit zero origins for record sequence, frame index, and logical ADC byte offset;
- ADC keys `dtype`, `byte_order`, `lane_count`, and `layout`, with the current closed tuple `int16`,
  `little`, `2`, and `group2_i_then_q`;
- radar-configuration keys `format`, `size_bytes`, and `sha256`, with format
  `ti_mmwave_legacy_cli.v1`, exact byte size, and SHA-256;
- required paired artifact schema **mmwcli.capture_session.v1**.

The hardware, ADC, and configuration-format fields are derived from the closed raw-capture
descriptor in the exact CFG-bound plan. They are not independently supplied stream labels. Encoder
construction revalidates the carried configuration snapshot against that complete plan before it
writes SESSION.

The frame size is positive, int16-aligned, and no greater than 64 MiB. The total expected byte count
must fit signed 64-bit file accounting.

JSON object keys must be unique and non-standard numeric constants are invalid. Every v1 JSON object
has an exact key set; unknown or missing fields are invalid. Extensions require a new schema, and
framing changes require a new protocol major. Numeric fields are JSON integers, not floats, strings,
booleans, or null.

## Configuration and frames

RADAR_CONFIG contains the exact UTF-8 radar.cfg bytes described by SESSION. It is non-empty and
limited to 4 MiB. Consumers verify its byte count and SHA-256 before accepting frames.

Each FRAME payload is one complete raw radar frame and must contain exactly the declared frame byte
count. A frame is emitted only after mmwcli has complete coverage for that logical frame. The record
does not carry a host-arrival timestamp: DCA1000 aggregation and UDP delivery time do not establish
radar sampling time.

SESSION and FRAME records are provisional until COMMIT. Consumers may process frames incrementally,
but absence of a valid commit means the stream result is incomplete.

## Terminal records

Terminal payloads are strict UTF-8 JSON with schema **mmwcli.capture_stream_terminal.v1** and a
4 KiB limit. Both outcomes carry the stream ID, emitted frame count, logical ADC byte count, and
SHA-256 of emitted frame payloads in frame-index order.

COMMIT is valid only after:

1. every planned frame and byte passed capture integrity checks;
2. radar and DCA1000 cleanup succeeded;
3. adc.bin, radar.cfg, and capture.json were synchronized and closed;
4. the capture-session directory was published without overwrite;
5. the published ADC size and SHA-256 matched the streamed frames.

COMMIT omits reason_code. ABORT includes exactly one non-empty stable reason code: cancelled,
backpressure, capture_failed, integrity_failed, cleanup_failed, or publish_failed. Empty, null, or
unknown substitutes are invalid. Detailed errors remain on stderr. ABORT is best effort; EOF,
transport failure, or any stream without a valid COMMIT is also an abort. A truncated SESSION or
RADAR_CONFIG write is therefore an aborted stream.

The filesystem transaction and the pipe cannot be atomic. COMMIT is sent after directory
publication. If COMMIT delivery fails, the published capture directory remains valid, while the
consumer rejects the uncommitted stream and mmwcli reports a stream error.

A terminal record becomes final only when it is followed by EOF. A producer must close the dedicated
data stream promptly after COMMIT or ABORT. Consumers apply a bounded EOF wait; trailing bytes,
records after a terminal, or a stalled open stream abort the stream result.

## WriterAt and backpressure guarantees

DCA1000 payloads arrive with 48-bit byte offsets and may be out of order. The existing receiver
normalizes accepted payloads to logical capture offset zero and writes them through `io.WriterAt`.
The implemented Mirror binds to the exact transactional output used by the capture session. It:

- splits writes that cross logical frame boundaries;
- tracks exact byte coverage and emits only complete frames in increasing index order;
- independently bounds in-flight frame count, buffered bytes, and the encoder queue;
- rejects overlaps, gaps, out-of-range offsets, missing prefixes, and reorder-window overflow;
- writes each fragment to the transactional capture artifact before adding it to provisional stream
  coverage.

It does not expose DCA UDP packets, infer frame phase from packet arrival, fill missing data, drop
frames, use an unbounded queue, or silently spill stream state to another file. The optional
`session.Run` hook requires the Mirror to be bound to that session's output, aborts it on capture
failure, and seals every frame through a bounded context before output publication.

A slow or disconnected consumer is a capture failure, not permission to lose records. Queue
exhaustion or write failure invokes the Mirror's callback on the shared capture context, entering
the existing bounded hardware cleanup; StartRecord is not retried. Mirror sealing requires a
deadline, and `StdoutEncoder` can close its owned handle independently to interrupt a blocked stream
writer.

Cancellation belongs to the acquisition control plane, not this producer-to-consumer record stream.
The application maps Mirror or pipe failure into the shared capture context, performs bounded radar
and DCA1000 cleanup, then attempts ABORT. Successful capture publishes `OUTDIR`, completes device
cleanup, emits COMMIT, and closes stdout; only COMMIT followed by EOF makes provisional FRAME records
final. Closing a data pipe or killing a process does not turn provisional frames into a commit.

## Exclusions

V1 does not define continuous capture, reconnect, resume, seeking, multiple consumers, fan-out,
compression, encryption, shared memory, clock synchronization, physical arrival timestamps, or a
stream-only transaction. It does not add device, process, or transport ownership to mmwcore.
