package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

func runSetup(arguments []string, stdout, stderr io.Writer) error {
	if len(arguments) == 0 || isHelp(arguments[0]) {
		printSetupHelp(stdout)
		return nil
	}
	switch arguments[0] {
	case "show":
		if len(arguments) != 2 || arguments[1] == "" || strings.HasPrefix(arguments[1], "-") {
			return usageError{message: "mmwcli setup show SETUP"}
		}
		setup, err := loadSetup(arguments[1])
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(setup)
	case "mount":
		return runSetupMount(arguments[1:], stderr)
	case "roi":
		return runSetupROI(arguments[1:], stderr)
	default:
		return usageError{message: "unknown setup command: " + arguments[0]}
	}
}

func runSetupROI(arguments []string, stderr io.Writer) error {
	const synopsis = "mmwcli setup roi SETUP --min-forward M --max-forward M --min-lateral M --max-lateral M --min-up M --max-up M"
	flags := newCommandFlagSet("setup roi", stderr, synopsis)
	minForward := flags.Float64("min-forward", math.NaN(), "minimum forward distance in metres")
	maxForward := flags.Float64("max-forward", math.NaN(), "maximum forward distance in metres")
	minLateral := flags.Float64("min-lateral", math.NaN(), "minimum lateral distance in metres")
	maxLateral := flags.Float64("max-lateral", math.NaN(), "maximum lateral distance in metres")
	minUp := flags.Float64("min-up", math.NaN(), "minimum height in metres")
	maxUp := flags.Float64("max-up", math.NaN(), "maximum height in metres")
	if len(arguments) < 1 || arguments[0] == "" || strings.HasPrefix(arguments[0], "-") {
		return usageError{message: synopsis}
	}
	if err := parseCommandFlags(flags, arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: synopsis}
	}
	roi := setupROI{
		Frame: levelROIFrame,
		MinM:  [3]float64{*minForward, *minLateral, *minUp},
		MaxM:  [3]float64{*maxForward, *maxLateral, *maxUp},
	}
	if err := validateROI(roi); err != nil {
		return usageError{message: err.Error()}
	}
	release, err := acquireHardwareLock(arguments[0])
	if err != nil {
		return err
	}
	defer release()
	setup, err := loadSetup(arguments[0])
	if err != nil {
		return err
	}
	setup.ROI = &roi
	return writeSetup(setup.path, setup)
}

func runSetupMount(arguments []string, stderr io.Writer) error {
	const synopsis = "mmwcli setup mount SETUP --height M --pitch 90"
	flags := newCommandFlagSet("setup mount", stderr, synopsis)
	height := flags.String("height", "", "radar height in metres")
	pitch := flags.String("pitch", "", "boresight pitch: 90 downward, 0 horizontal")
	if len(arguments) < 1 || arguments[0] == "" || strings.HasPrefix(arguments[0], "-") {
		return usageError{message: synopsis}
	}
	if err := parseCommandFlags(flags, arguments[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *height == "" || *pitch == "" {
		return usageError{message: synopsis}
	}
	heightM, err := strconv.ParseFloat(*height, 64)
	if err != nil {
		return usageError{message: "--height must be a number"}
	}
	pitchDeg, err := strconv.ParseFloat(*pitch, 64)
	if err != nil {
		return usageError{message: "--pitch must be a number"}
	}
	mount := setupMount{HeightM: heightM, PitchDeg: pitchDeg}
	if err := validateMount(mount); err != nil {
		return usageError{message: err.Error()}
	}
	release, err := acquireHardwareLock(arguments[0])
	if err != nil {
		return err
	}
	defer release()
	setup, err := loadSetup(arguments[0])
	if err != nil {
		return err
	}
	setup.Mount = mount
	if err := writeSetup(setup.path, setup); err != nil {
		return err
	}
	return nil
}

func printSetupHelp(writer io.Writer) {
	fmt.Fprintln(writer, "usage:")
	fmt.Fprintln(writer, "  mmwcli setup show SETUP")
	fmt.Fprintln(writer, "  mmwcli setup mount SETUP --height M --pitch 90")
	fmt.Fprintln(writer, "  mmwcli setup roi SETUP --min-forward M --max-forward M --min-lateral M --max-lateral M --min-up M --max-up M")
}
