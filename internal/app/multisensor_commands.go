package app

import (
	"fmt"
	"io"
	"strings"

	"mmwcli/internal/multisensorcapture"
)

func runMultisensor(arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 {
		return usageError{message: "multisensor requires check"}
	}
	if isHelp(arguments[0]) {
		printMultisensorHelp(stdout)
		return nil
	}
	if !strings.EqualFold(arguments[0], "check") {
		return usageError{message: "unknown multisensor command: " + arguments[0]}
	}
	return runMultisensorCheck(arguments[1:], stdout, stderr)
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
	fmt.Fprintln(writer, "usage: mmwcli multisensor check PLAN")
}
