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

var takeCommandNames = map[string]struct{}{
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

// BuildPlan validates the exact IWR6843 CFG snapshot consumed by mmwcore.
func BuildPlan(snapshot []byte) (Plan, error) {
	return buildPlan(snapshot)
}

func buildPlan(snapshot []byte) (Plan, error) {
	if !utf8.Valid(snapshot) {
		return Plan{}, errors.New("take CFG must be valid UTF-8")
	}
	commands, err := ParseConfig(bytes.NewReader(snapshot))
	if err != nil {
		return Plan{}, err
	}
	plan, err := commandPlan(commands)
	if err != nil {
		return Plan{}, err
	}
	contractCommands, err := parseTakeCommands(snapshot)
	if err != nil {
		return Plan{}, err
	}
	contractPlan, err := commandPlan(contractCommands)
	if err != nil {
		return Plan{}, fmt.Errorf("take CFG is not representable: %w", err)
	}
	if !sameGeometry(plan, contractPlan) {
		return Plan{}, errors.New("take CFG snapshot does not match the radar capture plan")
	}
	if err := validateTakeCommands(contractCommands, contractPlan); err != nil {
		return Plan{}, fmt.Errorf("take CFG is not representable: %w", err)
	}
	return plan, nil
}

// ValidatePlan binds an already-built plan to its exact CFG snapshot.
func ValidatePlan(snapshot []byte, actual Plan) error {
	expected, err := buildPlan(snapshot)
	if err != nil {
		return err
	}
	if !samePlan(expected, actual) {
		return errors.New("take CFG snapshot does not match the supplied radar capture plan")
	}
	return nil
}

func samePlan(left, right Plan) bool {
	return slices.Equal(left.ConfigurationCommands, right.ConfigurationCommands) &&
		left.DeclaredStartCommand == right.DeclaredStartCommand &&
		left.StartCommand == right.StartCommand &&
		left.StartWasSynthesized == right.StartWasSynthesized &&
		left.ExpectedDCADataFormat == right.ExpectedDCADataFormat &&
		left.BytesPerFrame == right.BytesPerFrame &&
		left.ExpectedBytes == right.ExpectedBytes &&
		left.HardwareLVDSEnabled == right.HardwareLVDSEnabled &&
		left.NumberOfFrames == right.NumberOfFrames &&
		left.FramePeriod == right.FramePeriod
}

// parseTakeCommands mirrors mmwcore's TI-CFG comment
// boundary: percent and hash comments are removed, unknown commands are
// ignored, and recognized contract commands retain their exact arguments.
func parseTakeCommands(snapshot []byte) ([]string, error) {
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
			return nil, errors.New("take supports legacy frameCfg only")
		}
		if _, recognized := takeCommandNames[fields[0]]; recognized {
			commands = append(commands, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read take configuration: %w", err)
	}
	return commands, nil
}

func sameGeometry(left, right Plan) bool {
	return left.ExpectedDCADataFormat == right.ExpectedDCADataFormat &&
		left.BytesPerFrame == right.BytesPerFrame &&
		left.ExpectedBytes == right.ExpectedBytes &&
		left.HardwareLVDSEnabled == right.HardwareLVDSEnabled &&
		left.NumberOfFrames == right.NumberOfFrames &&
		left.FramePeriod == right.FramePeriod
}

func validateTakeCommands(commands []string, plan Plan) error {
	adcFields, err := oneCommand(commands, "adcCfg")
	if err != nil {
		return err
	}
	if strings.Join(adcFields, " ") != "adcCfg 2 1" {
		return errors.New("take requires exact adcCfg 2 1")
	}
	channelFields, err := oneCommand(commands, "channelCfg")
	if err != nil {
		return err
	}
	rxMask, err := strconv.ParseUint(channelFields[1], 10, 8)
	if err != nil {
		return fmt.Errorf("parse take RX mask: %w", err)
	}
	if rxMask&(rxMask+1) != 0 {
		return errors.New("take cannot represent a sparse channelCfg RX mask")
	}

	profileFields, err := oneCommand(commands, "profileCfg")
	if err != nil {
		return err
	}
	profile, err := parseTakeProfile(profileFields)
	if err != nil {
		return err
	}
	if profile.samples%2 != 0 {
		return errors.New("group2_i_then_q capture session requires an even profileCfg numAdcSamples")
	}

	frame, err := parseFrame(commands)
	if err != nil {
		return err
	}
	frameFields, err := oneCommand(commands, "frameCfg")
	if err != nil {
		return err
	}
	framePeriodMilliseconds, err := parseDecimal(frameFields[5], "frameCfg periodicity")
	if err != nil {
		return err
	}
	framePeriodSeconds := framePeriodMilliseconds / 1e3
	if math.IsInf(framePeriodSeconds, 0) || framePeriodSeconds <= 0 {
		return errors.New("take scaled frameCfg periodicity must be finite and positive")
	}
	frameTriggerDelay, err := parseDecimal(frameFields[7], "frameCfg trigger delay")
	if err != nil {
		return err
	}
	if frameTriggerDelay != 0 {
		return errors.New("take requires zero frameCfg trigger delay")
	}
	profiles, err := parseProfiles(commands)
	if err != nil {
		return err
	}
	_, enabledTransmitters, err := parseChannels(commands)
	if err != nil {
		return err
	}
	ranges, err := parseChirps(commands, profiles, enabledTransmitters)
	if err != nil {
		return err
	}
	if err := validateTakeChirps(commands, frame, ranges); err != nil {
		return err
	}
	chirpsPerFrame, err := checkedExpectedMultiply(
		frame.chirpEnd-frame.chirpStart+1,
		frame.loops,
		"take chirps per frame",
	)
	if err != nil {
		return err
	}
	activeSeconds := (profile.idleSeconds + profile.rampEndSeconds) * float64(chirpsPerFrame)
	if math.IsInf(activeSeconds, 0) || framePeriodSeconds < activeSeconds {
		return errors.New("take frame periodicity is shorter than the active chirp time")
	}
	return nil
}

func oneCommand(commands []string, name string) ([]string, error) {
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
		return nil, fmt.Errorf("take requires exactly one %s", name)
	}
	return result, nil
}

type takeProfile struct {
	idleSeconds    float64
	rampEndSeconds float64
	samples        uint64
}

func parseTakeProfile(fields []string) (takeProfile, error) {
	if len(fields) != 15 {
		return takeProfile{}, errors.New("take profileCfg must contain fourteen arguments")
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
		raw, err := parseDecimal(fields[field.index], "profileCfg "+field.name)
		if err != nil {
			return takeProfile{}, err
		}
		value := raw * field.multiplier
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			return takeProfile{}, fmt.Errorf(
				"take scaled profileCfg %s must be finite and positive",
				field.name,
			)
		}
		values[field.index] = value
	}
	if values[4] >= values[5] {
		return takeProfile{}, errors.New("take profileCfg ADC start time must precede ramp end time")
	}
	samples, err := strconv.ParseUint(fields[10], 10, 16)
	if err != nil || samples == 0 {
		return takeProfile{}, errors.New("take profileCfg numAdcSamples must be positive")
	}
	return takeProfile{
		idleSeconds:    values[3],
		rampEndSeconds: values[5],
		samples:        samples,
	}, nil
}

func validateTakeChirps(
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
			value, err := parseDecimal(fields[index], "chirpCfg variation")
			if err != nil {
				return err
			}
			if value != 0 {
				return errors.New("take does not support nonzero chirpCfg variations")
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
			return fmt.Errorf("take frame chirp %d is undefined", index)
		}
		if bits.OnesCount64(configured.txEnable) != 1 {
			return errors.New("take requires each frame chirp to enable exactly one TX")
		}
		if seenTransmitters&configured.txEnable != 0 {
			return errors.New("take requires each active TX exactly once per frame loop")
		}
		seenTransmitters |= configured.txEnable
	}
	return nil
}

func parseDecimal(token, name string) (float64, error) {
	if strings.ContainsAny(token, "xXpP_") {
		return 0, fmt.Errorf("take %s must use decimal floating-point syntax", name)
	}
	value, err := strconv.ParseFloat(token, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("take %s must be finite decimal floating-point", name)
	}
	return value, nil
}
