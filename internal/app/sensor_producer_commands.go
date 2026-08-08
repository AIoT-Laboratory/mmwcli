package app

import (
	"fmt"
	"io"
	"strings"

	"mmwcli/internal/fixedframeproducer"
	"mmwcli/internal/multisensorcapture"
)

func runSensorProducer(
	arguments []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	if len(arguments) == 0 {
		return usageError{message: "sensor-producer requires fixed-frames"}
	}
	if isHelp(arguments[0]) {
		printSensorProducerHelp(stdout)
		return nil
	}
	if !strings.EqualFold(arguments[0], "fixed-frames") {
		return usageError{message: "unknown sensor-producer command: " + arguments[0]}
	}
	return runFixedFramesProducer(arguments[1:], stdin, stdout, stderr)
}

func runFixedFramesProducer(
	arguments []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	flags := newCommandFlagSet(
		"sensor-producer fixed-frames",
		stderr,
		"mmwcli sensor-producer fixed-frames --plan PLAN --source SOURCE --frame-bytes N -- CAMERA_COMMAND [ARG...]",
	)
	planPath := flags.String("plan", "", "strict mmwcli.multisensor_plan.v1 JSON file")
	sourceID := flags.String("source", "", "exact source_id from PLAN")
	frameBytes := flags.Uint64("frame-bytes", 0, "bytes in each headerless camera frame")

	separator := commandSeparator(arguments)
	flagArguments := arguments
	var cameraArgv []string
	if separator >= 0 {
		flagArguments = arguments[:separator]
		cameraArgv = arguments[separator+1:]
	}
	if err := parseCommandFlags(flags, flagArguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{
			message: "unexpected fixed-frames arguments before --: " + strings.Join(flags.Args(), " "),
		}
	}
	if separator < 0 || len(cameraArgv) == 0 {
		return usageError{message: "fixed-frames requires -- CAMERA_COMMAND [ARG...]"}
	}
	if *planPath == "" || *sourceID == "" || *frameBytes == 0 {
		return usageError{
			message: "fixed-frames requires --plan PLAN, --source SOURCE, and --frame-bytes N > 0",
		}
	}

	plan, err := multisensorcapture.LoadPlan(*planPath)
	if err != nil {
		return err
	}
	source, err := exactProducerSource(plan, *sourceID)
	if err != nil {
		return err
	}
	ctx, cancel := hardwareSignalContext()
	defer cancel()
	return fixedframeproducer.Run(
		ctx,
		source,
		*frameBytes,
		cameraArgv,
		stdin,
		stdout,
		stderr,
	)
}

func exactProducerSource(
	plan multisensorcapture.Plan,
	sourceID string,
) (multisensorcapture.SourcePlan, error) {
	for _, source := range plan.Sources {
		if source.SourceID == sourceID {
			return source, nil
		}
	}
	return multisensorcapture.SourcePlan{}, fmt.Errorf(
		"multisensor plan has no exact source_id %q",
		sourceID,
	)
}

func commandSeparator(arguments []string) int {
	for index, argument := range arguments {
		if argument == "--" {
			return index
		}
	}
	return -1
}

func printSensorProducerHelp(writer io.Writer) {
	fmt.Fprintln(
		writer,
		"usage: mmwcli sensor-producer fixed-frames --plan PLAN --source SOURCE --frame-bytes N -- CAMERA_COMMAND [ARG...]",
	)
}
