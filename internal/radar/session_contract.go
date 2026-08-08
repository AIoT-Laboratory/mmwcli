package radar

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

var captureSessionV1CommandNames = map[string]struct{}{
	"flushCfg":          {},
	"dfeDataOutputMode": {},
	"channelCfg":        {},
	"adcCfg":            {},
	"adcbufCfg":         {},
	"profileCfg":        {},
	"chirpCfg":          {},
	"frameCfg":          {},
	"lvdsStreamCfg":     {},
}

// BuildCaptureSessionV1Plan builds the StudioCLI capture plan and verifies
// that the exact CFG snapshot is representable by the capture-session v1
// contract consumed by mmwcore.
func BuildCaptureSessionV1Plan(snapshot []byte, mode ConfigurationMode) (CapturePlan, error) {
	if mode != FullConfiguration {
		return CapturePlan{}, errors.New("capture session v1 requires a full radar configuration")
	}
	return buildCaptureSessionV1Plan(xwr68xxFamily, snapshot)
}

// BuildCaptureSessionV1PlanForFamily builds the sole full-configuration
// capture-session v1 plan for an explicitly selected closed family.
func BuildCaptureSessionV1PlanForFamily(
	family DeviceFamily,
	snapshot []byte,
) (CapturePlan, error) {
	if !family.valid() {
		return CapturePlan{}, errors.New("invalid radar device family")
	}
	return buildCaptureSessionV1Plan(family, snapshot)
}

func buildCaptureSessionV1Plan(family DeviceFamily, snapshot []byte) (CapturePlan, error) {
	if !utf8.Valid(snapshot) {
		return CapturePlan{}, errors.New("capture session v1 CFG must be valid UTF-8")
	}
	commands, err := ParseConfig(bytes.NewReader(snapshot))
	if err != nil {
		return CapturePlan{}, err
	}
	plan, err := buildCapturePlan(StudioCLI, family, commands, FullConfiguration)
	if err != nil {
		return CapturePlan{}, err
	}
	contractCommands, err := parseCaptureSessionV1Commands(snapshot)
	if err != nil {
		return CapturePlan{}, err
	}
	contractPlan, err := buildCapturePlan(StudioCLI, family, contractCommands, FullConfiguration)
	if err != nil {
		return CapturePlan{}, fmt.Errorf("capture session v1 CFG is not representable: %w", err)
	}
	if !sameCaptureGeometry(plan, contractPlan) {
		return CapturePlan{}, errors.New("capture session v1 CFG snapshot does not match the radar capture plan")
	}
	if err := validateCaptureSessionV1Subset(family, contractCommands, contractPlan); err != nil {
		return CapturePlan{}, fmt.Errorf("capture session v1 CFG is not representable: %w", err)
	}
	return plan, nil
}

// ValidateCaptureSessionV1Plan binds an already-built capture plan to the
// exact CFG snapshot that will be carried by a capture-session v1 artifact or
// stream. It rejects semantic differences even when they preserve byte
// geometry.
func ValidateCaptureSessionV1Plan(snapshot []byte, actual CapturePlan) error {
	if actual.Mode != FullConfiguration {
		return errors.New("capture session v1 requires a full radar configuration")
	}
	family := actual.DeviceFamily()
	if !family.valid() {
		return errors.New("capture session v1 plan has an invalid radar device family")
	}
	expected, err := buildCaptureSessionV1Plan(family, snapshot)
	if err != nil {
		return err
	}
	if !sameCapturePlan(expected, actual) {
		return errors.New("capture session v1 CFG snapshot does not match the supplied radar capture plan")
	}
	return nil
}

func sameCapturePlan(left, right CapturePlan) bool {
	return left.Dialect == right.Dialect &&
		left.RawCapture == right.RawCapture &&
		left.Mode == right.Mode &&
		slices.Equal(left.ConfigurationCommands, right.ConfigurationCommands) &&
		left.DeclaredStartCommand == right.DeclaredStartCommand &&
		left.StartCommand == right.StartCommand &&
		left.StartWasSynthesized == right.StartWasSynthesized &&
		left.ExpectedDCADataFormat == right.ExpectedDCADataFormat &&
		left.BytesPerFrame == right.BytesPerFrame &&
		left.ExpectedBytes == right.ExpectedBytes &&
		left.HardwareLVDSEnabled == right.HardwareLVDSEnabled &&
		left.InfiniteFrames == right.InfiniteFrames &&
		left.NumberOfFrames == right.NumberOfFrames &&
		left.FramePeriod == right.FramePeriod
}

// parseCaptureSessionV1Commands mirrors mmwcore's public TI-CFG comment
// boundary: percent and hash comments are removed, unknown commands are
// ignored, and recognized contract commands retain their exact arguments.
func parseCaptureSessionV1Commands(snapshot []byte) ([]string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(snapshot))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	commands := make([]string, 0, 16)
	for scanner.Scan() {
		line := scanner.Text()
		if comment := strings.IndexByte(line, '%'); comment >= 0 {
			line = line[:comment]
		}
		if comment := strings.IndexByte(line, '#'); comment >= 0 {
			line = line[:comment]
		}
		line = strings.TrimSpace(line)
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "advFrameCfg" || fields[0] == "subFrameCfg" {
			return nil, errors.New("capture session v1 supports legacy frameCfg only")
		}
		if _, recognized := captureSessionV1CommandNames[fields[0]]; recognized {
			commands = append(commands, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read capture session v1 configuration: %w", err)
	}
	return commands, nil
}

func sameCaptureGeometry(left, right CapturePlan) bool {
	return left.RawCapture == right.RawCapture &&
		left.Mode == right.Mode &&
		left.ExpectedDCADataFormat == right.ExpectedDCADataFormat &&
		left.BytesPerFrame == right.BytesPerFrame &&
		left.ExpectedBytes == right.ExpectedBytes &&
		left.HardwareLVDSEnabled == right.HardwareLVDSEnabled &&
		left.InfiniteFrames == right.InfiniteFrames &&
		left.NumberOfFrames == right.NumberOfFrames &&
		left.FramePeriod == right.FramePeriod
}

func validateCaptureSessionV1Subset(
	family DeviceFamily,
	commands []string,
	plan CapturePlan,
) error {
	if plan.InfiniteFrames || plan.NumberOfFrames == 0 || plan.ExpectedBytes <= 0 {
		return errors.New("mmwcli session publication currently requires a finite frame count")
	}
	adcFields, err := singleCaptureSessionCommand(commands, "adcCfg")
	if err != nil {
		return err
	}
	if strings.Join(adcFields, " ") != "adcCfg 2 1" {
		return errors.New("capture session v1 requires exact adcCfg 2 1")
	}
	channelFields, err := singleCaptureSessionCommand(commands, "channelCfg")
	if err != nil {
		return err
	}
	rxMask, err := strconv.ParseUint(channelFields[1], 10, 8)
	if err != nil {
		return fmt.Errorf("parse capture session v1 RX mask: %w", err)
	}
	if rxMask&(rxMask+1) != 0 {
		return errors.New("capture session v1 cannot represent a sparse channelCfg RX mask")
	}

	profileFields, err := singleCaptureSessionCommand(commands, "profileCfg")
	if err != nil {
		return err
	}
	profile, err := captureSessionV1Profile(profileFields)
	if err != nil {
		return err
	}
	if profile.samples%2 != 0 {
		return errors.New("group2_i_then_q capture session requires an even profileCfg numAdcSamples")
	}

	frame, err := parseFrameForFamily(family, commands)
	if err != nil {
		return err
	}
	frameFields, err := singleCaptureSessionCommand(commands, "frameCfg")
	if err != nil {
		return err
	}
	framePeriodMilliseconds, err := parseCaptureSessionDecimal(frameFields[5], "frameCfg periodicity")
	if err != nil {
		return err
	}
	framePeriodSeconds := framePeriodMilliseconds / 1e3
	if math.IsInf(framePeriodSeconds, 0) || framePeriodSeconds <= 0 {
		return errors.New("capture session v1 scaled frameCfg periodicity must be finite and positive")
	}
	frameTriggerDelay, err := parseCaptureSessionDecimal(frameFields[7], "frameCfg trigger delay")
	if err != nil {
		return err
	}
	if frameTriggerDelay != 0 {
		return errors.New("capture session v1 requires zero frameCfg trigger delay")
	}
	profiles, err := parseProfileSamples(family, commands)
	if err != nil {
		return err
	}
	_, enabledTransmitters, err := parseChannelConfigurationForFamily(family, commands)
	if err != nil {
		return err
	}
	ranges, err := parseChirpProfileRangesForFamily(family, commands, profiles, enabledTransmitters)
	if err != nil {
		return err
	}
	if err := validateCaptureSessionV1Chirps(commands, frame, ranges); err != nil {
		return err
	}
	chirpsPerFrame, err := checkedExpectedMultiply(
		frame.chirpEnd-frame.chirpStart+1,
		frame.loops,
		"capture session v1 chirps per frame",
	)
	if err != nil {
		return err
	}
	activeSeconds := (profile.idleSeconds + profile.rampEndSeconds) * float64(chirpsPerFrame)
	if math.IsInf(activeSeconds, 0) || framePeriodSeconds < activeSeconds {
		return errors.New("capture session v1 frame periodicity is shorter than the active chirp time")
	}
	return nil
}

func singleCaptureSessionCommand(commands []string, name string) ([]string, error) {
	var result []string
	count := 0
	for _, command := range commands {
		fields := strings.Fields(command)
		if len(fields) == 0 || fields[0] != name {
			continue
		}
		result = fields
		count++
	}
	if count != 1 {
		return nil, fmt.Errorf("capture session v1 requires exactly one %s", name)
	}
	return result, nil
}

type captureSessionProfile struct {
	idleSeconds    float64
	rampEndSeconds float64
	samples        uint64
}

func captureSessionV1Profile(fields []string) (captureSessionProfile, error) {
	if len(fields) != 15 {
		return captureSessionProfile{}, errors.New("capture session v1 profileCfg must contain fourteen arguments")
	}
	physical := []struct {
		name       string
		index      int
		multiplier float64
	}{
		{name: "start frequency", index: 2, multiplier: 1e9},
		{name: "idle time", index: 3, multiplier: 1e-6},
		{name: "ADC start time", index: 4, multiplier: 1e-6},
		{name: "ramp end time", index: 5, multiplier: 1e-6},
		{name: "frequency slope", index: 8, multiplier: 1e12},
		{name: "ADC sample rate", index: 11, multiplier: 1e3},
	}
	values := make(map[int]float64, len(physical))
	for _, field := range physical {
		raw, err := parseCaptureSessionDecimal(fields[field.index], "profileCfg "+field.name)
		if err != nil {
			return captureSessionProfile{}, err
		}
		value := raw * field.multiplier
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return captureSessionProfile{}, fmt.Errorf(
				"capture session v1 scaled profileCfg %s must be finite and positive",
				field.name,
			)
		}
		values[field.index] = value
	}
	if values[4] >= values[5] {
		return captureSessionProfile{}, errors.New("capture session v1 profileCfg ADC start time must precede ramp end time")
	}
	samples, err := strconv.ParseUint(fields[10], 10, 16)
	if err != nil || samples == 0 {
		return captureSessionProfile{}, errors.New("capture session v1 profileCfg numAdcSamples must be positive")
	}
	return captureSessionProfile{
		idleSeconds:    values[3],
		rampEndSeconds: values[5],
		samples:        samples,
	}, nil
}

func validateCaptureSessionV1Chirps(
	commands []string,
	frame frameConfiguration,
	ranges []chirpProfileRange,
) error {
	for _, command := range commands {
		fields := strings.Fields(command)
		if len(fields) == 0 || fields[0] != "chirpCfg" {
			continue
		}
		for index := 4; index <= 7; index++ {
			value, err := parseCaptureSessionDecimal(fields[index], "chirpCfg variation")
			if err != nil {
				return err
			}
			if value != 0 {
				return errors.New("capture session v1 does not support nonzero chirpCfg variations")
			}
		}
	}
	byIndex := make(map[uint64]chirpProfileRange, maximumChirpIndex+1)
	for _, configured := range ranges {
		for index := configured.start; index <= configured.end; index++ {
			byIndex[index] = configured
		}
	}
	seenTransmitters := uint64(0)
	for index := frame.chirpStart; index <= frame.chirpEnd; index++ {
		configured, found := byIndex[index]
		if !found {
			return fmt.Errorf("capture session v1 frame chirp %d is undefined", index)
		}
		if bits.OnesCount64(configured.txEnable) != 1 {
			return errors.New("capture session v1 requires each frame chirp to enable exactly one TX")
		}
		if seenTransmitters&configured.txEnable != 0 {
			return errors.New("capture session v1 requires each active TX exactly once per frame loop")
		}
		seenTransmitters |= configured.txEnable
	}
	return nil
}

func parseCaptureSessionDecimal(token, name string) (float64, error) {
	if strings.ContainsAny(token, "xXpP_") {
		return 0, fmt.Errorf("capture session v1 %s must use decimal floating-point syntax", name)
	}
	value, err := strconv.ParseFloat(token, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("capture session v1 %s must be finite decimal floating-point", name)
	}
	return value, nil
}
