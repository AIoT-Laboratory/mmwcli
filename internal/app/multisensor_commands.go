package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"os"
	"strconv"
	"strings"
	"time"

	"mmwcli/internal/fixedframeproducer"
	"mmwcli/internal/multisensor"
	"mmwcli/internal/multisensorcapture"
)

func runMultisensor(arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		return usageError{message: "multisensor requires init or check"}
	}
	if isHelp(arguments[0]) {
		printMultisensorHelp(stdout)
		return nil
	}
	switch strings.ToLower(arguments[0]) {
	case "init":
		return runMultisensorInit(arguments[1:], stdout, stderr)
	case "check":
		return runMultisensorCheck(arguments[1:], stdout, stderr)
	default:
		return usageError{message: "unknown multisensor command: " + arguments[0]}
	}
}

func runMultisensorInit(arguments []string, stdout, stderr io.Writer) error {
	flags := newCommandFlagSet(
		"multisensor init",
		stderr,
		"mmwcli multisensor init PLAN [--source-id ID] --format FORMAT --frame-bytes N "+
			"--max-items N [--required=false] [--payload FILE] -- CAMERA_COMMAND [ARG...]",
	)
	sourceID := flags.String("source-id", "camera-0", "camera source_id")
	format := flags.String("format", "", "explicit payload format contract")
	frameBytes := flags.Uint64("frame-bytes", 0, "bytes in each headerless camera frame")
	maxItems := flags.Uint64("max-items", 0, "maximum camera frames in the capture")
	required := flags.Bool("required", true, "require this camera to complete the aggregate capture")
	payloadFilename := flags.String("payload", "frames.bin", "published camera payload filename")
	if len(arguments) != 0 && isHelp(arguments[0]) {
		return parseCommandFlags(flags, arguments)
	}
	if len(arguments) == 0 || strings.HasPrefix(arguments[0], "-") {
		return usageError{message: "multisensor init requires PLAN before its options"}
	}
	if len(arguments) > 1 && isHelp(arguments[1]) {
		return parseCommandFlags(flags, arguments[1:])
	}
	planPath := arguments[0]
	separator := commandSeparator(arguments[1:])
	if separator < 0 {
		return usageError{message: "multisensor init requires -- CAMERA_COMMAND [ARG...]"}
	}
	separator++
	if err := parseCommandFlags(flags, arguments[1:separator]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected multisensor init arguments: " + strings.Join(flags.Args(), " ")}
	}
	cameraArgv := append([]string(nil), arguments[separator+1:]...)
	if len(cameraArgv) == 0 || cameraArgv[0] == "" {
		return usageError{message: "multisensor init requires CAMERA_COMMAND after --"}
	}
	if strings.TrimSpace(*format) == "" || *frameBytes == 0 || *maxItems == 0 {
		return usageError{message: "multisensor init requires --format, --frame-bytes N > 0, and --max-items N > 0"}
	}
	if *frameBytes > fixedframeproducer.MaximumFrameBytes {
		return usageError{message: fmt.Sprintf(
			"--frame-bytes exceeds the fixed-frame producer maximum %d",
			fixedframeproducer.MaximumFrameBytes,
		)}
	}
	high, payloadBytes := bits.Mul64(*frameBytes, *maxItems)
	if high != 0 {
		return usageError{message: "--frame-bytes * --max-items overflows uint64"}
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve mmwcli executable: %w", err)
	}
	producerArgv := []string{
		executable,
		"sensor-producer",
		"fixed-frames",
		"--plan",
		planPath,
		"--source",
		*sourceID,
		"--frame-bytes",
		strconv.FormatUint(*frameBytes, 10),
		"--",
	}
	producerArgv = append(producerArgv, cameraArgv...)
	plan := multisensorcapture.Plan{
		Schema: multisensorcapture.PlanSchema,
		Sources: []multisensorcapture.SourcePlan{{
			SourceID: *sourceID, Kind: multisensor.SourceCamera, Required: *required,
			Argv: producerArgv, QueueSize: 0,
			Producer: multisensor.Producer{
				Name: fixedframeproducer.ProducerName, Version: fixedframeproducer.ProducerVersion,
			},
			Limits: multisensor.SourceLimits{
				MaxItems: *maxItems, MaxItemBytes: *frameBytes, MaxPayloadBytes: payloadBytes,
			},
			Payload: multisensor.PayloadContract{Filename: *payloadFilename, Format: *format},
			Clock: multisensor.Clock{
				ClockID: multisensor.DeliveryObservedClockID(*sourceID),
				TickHz:  uint64(time.Second / time.Nanosecond), WrapTicks: 0,
				TimestampSemantics: multisensor.TimestampDeliveryObserved,
			},
			SyncEventSemantics:  multisensorcapture.SyncEventSemanticsNone,
			ApplicationMetadata: multisensor.ApplicationMetadata{},
		}},
		ApplicationMetadata: multisensor.ApplicationMetadata{},
	}
	encoded, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("encode multisensor plan: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := multisensorcapture.ParsePlan(encoded); err != nil {
		return usageError{message: "generated multisensor plan is invalid: " + err.Error()}
	}
	if err := writeNewMultisensorPlan(planPath, encoded); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "created multisensor plan: %s\n", planPath)
	fmt.Fprintln(stdout, "next:")
	fmt.Fprintf(stdout, "  mmwcli multisensor check %s\n", planPath)
	fmt.Fprintf(
		stdout,
		"  mmwcli studio-cli capture CFG OUTDIR --port PORT --multisensor-plan %s [options]\n",
		planPath,
	)
	fmt.Fprintf(
		stdout,
		"  mmwcli debug-cli capture CFG OUTDIR --family FAMILY --enhanced-port PORT "+
			"--bss-fw BSS --mss-fw MSS (--d2xx-serial BASE | --d2xx-description BASE) "+
			"--multisensor-plan %s [options]\n",
		planPath,
	)
	return nil
}

func writeNewMultisensorPlan(path string, encoded []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create multisensor plan %s: %w", path, err)
	}
	written, writeErr := file.Write(encoded)
	if writeErr == nil && written != len(encoded) {
		writeErr = io.ErrShortWrite
	}
	closeErr := file.Close()
	if writeErr == nil && closeErr == nil {
		return nil
	}
	removeErr := os.Remove(path)
	return errors.Join(
		fmt.Errorf("write multisensor plan %s: %w", path, errors.Join(writeErr, closeErr)),
		removeErr,
	)
}

func runMultisensorCheck(arguments []string, stdout, stderr io.Writer) error {
	flags := newCommandFlagSet(
		"multisensor check",
		stderr,
		"mmwcli multisensor check PLAN",
	)
	if err := parseCommandFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return usageError{message: "multisensor check requires exactly one PLAN"}
	}

	plan, err := multisensorcapture.LoadPlan(flags.Arg(0))
	if err != nil {
		return err
	}
	required := 0
	for index, source := range plan.Sources {
		if source.Required {
			required++
		}
		fmt.Fprintf(
			stdout,
			"source[%d]: id=%s kind=%s required=%t producer=%s@%s payload=%s format=%s "+
				"clock=%s tick_hz=%d wrap_ticks=%d timestamp_semantics=%s "+
				"max_items=%d max_item_bytes=%d max_payload_bytes=%d\n",
			index,
			source.SourceID,
			source.Kind,
			source.Required,
			source.Producer.Name,
			source.Producer.Version,
			source.Payload.Filename,
			source.Payload.Format,
			source.Clock.ClockID,
			source.Clock.TickHz,
			source.Clock.WrapTicks,
			source.Clock.TimestampSemantics,
			source.Limits.MaxItems,
			source.Limits.MaxItemBytes,
			source.Limits.MaxPayloadBytes,
		)
	}
	fmt.Fprintf(
		stdout,
		"sources: total=%d required=%d optional=%d\n",
		len(plan.Sources),
		required,
		len(plan.Sources)-required,
	)
	fmt.Fprintln(stdout, "multisensor plan check passed (offline; no processes or hardware accessed)")
	return nil
}

func printMultisensorHelp(writer io.Writer) {
	fmt.Fprintln(writer, "usage: mmwcli multisensor init PLAN [options] -- CAMERA_COMMAND [ARG...]")
	fmt.Fprintln(writer, "usage: mmwcli multisensor check PLAN")
}
