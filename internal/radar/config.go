package radar

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ConfigurationMode selects whether a capture will send the parsed RF/LVDS
// configuration or reuse the configuration already held by the device.
type ConfigurationMode uint8

const (
	FullConfiguration ConfigurationMode = iota
	ReuseConfiguration

	xwr68xxADCBufBytes        = uint64(32 * 1024)
	adcBufChannelAlignment    = uint64(16)
	complex16BytesPerSample   = uint64(4)
	cbuffMinimumTransferSize  = uint64(64)
	cbuffMaximumTransferSize  = uint64(0x3fff * 2)
	xwr68xxMaximumChirpIndex  = uint64(511)
	xwr68xxMaximumFrameLoops  = uint64(255)
	xwr68xxMinimumFramePeriod = 300 * time.Microsecond
	xwr68xxMaximumFramePeriod = 1342 * time.Millisecond
)

// CapturePlan is the hardware-independent result of CFG parsing and preflight.
// ConfigurationCommands never contains sensorStart. StartCommand is the exact
// command that an orchestrator should send only after its data sink is armed.
type CapturePlan struct {
	Dialect               Dialect
	Mode                  ConfigurationMode
	ConfigurationCommands []string
	DeclaredStartCommand  string
	StartCommand          string
	StartWasSynthesized   bool
	ExpectedDCADataFormat int
	// BytesPerFrame is the exact headerless complex16 LVDS payload produced by
	// one frame. It remains populated for infinite plans even though their total
	// ExpectedBytes is necessarily unknown.
	BytesPerFrame int64
	// ExpectedBytes is the exact raw ADC payload size for a finite frame plan.
	// Infinite frame plans use zero because they have no finite expected size.
	ExpectedBytes       int64
	HardwareLVDSEnabled bool
	InfiniteFrames      bool
	NumberOfFrames      uint16
	FramePeriod         time.Duration
}

// ParseConfig reads TI-style command files without touching hardware. Blank
// lines and lines beginning with %, #, or // are ignored. A whitespace-prefixed
// // suffix is treated as an inline comment.
func ParseConfig(reader io.Reader) ([]string, error) {
	if reader == nil {
		return nil, errors.New("configuration reader is nil")
	}

	scanner := bufio.NewScanner(reader)
	// Configuration lines are normally short. This bound makes malformed input
	// deterministic while remaining well above any supported command length.
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	commands := make([]string, 0, 32)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "%") ||
			strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if comment := strings.Index(line, " //"); comment >= 0 {
			line = strings.TrimSpace(line[:comment])
		}
		if line != "" {
			commands = append(commands, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read radar configuration: %w", err)
	}
	return commands, nil
}

// ParseConfigFile is the filesystem convenience wrapper around ParseConfig.
func ParseConfigFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open radar configuration %s: %w", path, err)
	}
	defer file.Close()
	return ParseConfig(file)
}

// BuildCapturePlan validates a raw ADC legacy-frame configuration before any
// hardware I/O. ReuseConfiguration still validates the supplied CFG locally,
// but selects sensorStart 0 and does not require flushCfg to be present.
func BuildCapturePlan(dialect Dialect, commands []string, mode ConfigurationMode) (CapturePlan, error) {
	if !dialect.valid() {
		return CapturePlan{}, errors.New("invalid radar CLI dialect")
	}
	if mode != FullConfiguration && mode != ReuseConfiguration {
		return CapturePlan{}, fmt.Errorf("invalid configuration mode %d", mode)
	}

	copyOfCommands := append([]string(nil), commands...)
	if err := rejectCaptureLifecycleCommands(copyOfCommands); err != nil {
		return CapturePlan{}, err
	}
	if err := dialect.ValidateConfiguration(copyOfCommands, mode == FullConfiguration); err != nil {
		return CapturePlan{}, err
	}

	configuration, declaredStart, synthesized, err := splitStart(copyOfCommands)
	if err != nil {
		return CapturePlan{}, err
	}
	if mode == FullConfiguration && declaredStart == "sensorStart 0" {
		return CapturePlan{}, errors.New("full configuration must use sensorStart; sensorStart 0 is reserved for reuse without reconfiguration")
	}

	if err := validateLegacyMode(configuration); err != nil {
		return CapturePlan{}, err
	}
	if mode == FullConfiguration {
		if err := validateLegacyCommandOrder(configuration); err != nil {
			return CapturePlan{}, err
		}
	}
	dcaFormat, err := validateADC(configuration)
	if err != nil {
		return CapturePlan{}, err
	}
	if err := validateADCBuf(dialect, configuration); err != nil {
		return CapturePlan{}, err
	}
	if err := validateHardwareLVDS(configuration); err != nil {
		return CapturePlan{}, err
	}
	frame, err := parseFrame(configuration)
	if err != nil {
		return CapturePlan{}, err
	}
	if _, err := frameSpan(frame.frames, frame.period); err != nil {
		return CapturePlan{}, err
	}
	bytesPerFrame, expectedBytes, err := deriveExpectedBytes(dialect, configuration, frame)
	if err != nil {
		return CapturePlan{}, err
	}

	start := declaredStart
	if start == "" {
		start = "sensorStart"
	}
	if mode == ReuseConfiguration {
		start = "sensorStart 0"
	}
	return CapturePlan{
		Dialect:               dialect,
		Mode:                  mode,
		ConfigurationCommands: append([]string(nil), configuration...),
		DeclaredStartCommand:  declaredStart,
		StartCommand:          start,
		StartWasSynthesized:   synthesized,
		ExpectedDCADataFormat: dcaFormat,
		BytesPerFrame:         bytesPerFrame,
		ExpectedBytes:         expectedBytes,
		HardwareLVDSEnabled:   true,
		InfiniteFrames:        frame.frames == 0,
		NumberOfFrames:        frame.frames,
		FramePeriod:           frame.period,
	}, nil
}

func rejectCaptureLifecycleCommands(commands []string) error {
	for _, command := range commands {
		if isCommand(command, "sensorStop") {
			return fmt.Errorf("sensorStop is managed by the capture coordinator and must not appear in a capture CFG: %s", command)
		}
	}
	return nil
}

func splitStart(commands []string) ([]string, string, bool, error) {
	startIndex := -1
	for index, command := range commands {
		if !isCommand(command, "sensorStart") {
			continue
		}
		if command != "sensorStart" && command != "sensorStart 0" {
			return nil, "", false, fmt.Errorf("capture accepts only exact sensorStart or sensorStart 0: %s", command)
		}
		if startIndex >= 0 {
			return nil, "", false, errors.New("capture configuration may contain only one sensorStart")
		}
		startIndex = index
	}
	if startIndex < 0 {
		return append([]string(nil), commands...), "", true, nil
	}
	if startIndex != len(commands)-1 {
		return nil, "", false, errors.New("sensorStart must be the final configuration command")
	}
	return append([]string(nil), commands[:startIndex]...), commands[startIndex], false, nil
}

func validateLegacyMode(commands []string) error {
	count := 0
	for _, command := range commands {
		if !isCommand(command, "dfeDataOutputMode") {
			continue
		}
		if err := requireExactName(command, "dfeDataOutputMode"); err != nil {
			return err
		}
		fields := strings.Fields(command)
		mode, err := parseIntField(fields, 1, 2)
		if err != nil {
			return fmt.Errorf("cannot parse dfeDataOutputMode %q: %w", command, err)
		}
		if mode != 1 {
			return fmt.Errorf("only legacy frame mode dfeDataOutputMode 1 is supported: %s", command)
		}
		count++
	}
	if count != 1 {
		return errors.New("configuration must contain exactly one dfeDataOutputMode 1")
	}
	return nil
}

func validateLegacyCommandOrder(commands []string) error {
	hasChannel := false
	hasProfile := false
	hasChirp := false
	for _, command := range commands {
		hasChannel = hasChannel || isCommand(command, "channelCfg")
		hasProfile = hasProfile || isCommand(command, "profileCfg")
		hasChirp = hasChirp || isCommand(command, "chirpCfg")
	}
	modeSeen := false
	channelSeen := false
	profileSeen := false
	chirpSeen := false
	frameSeen := false
	for _, command := range commands {
		switch {
		case isCommand(command, "dfeDataOutputMode"):
			modeSeen = true
		case isCommand(command, "channelCfg"):
			channelSeen = true
		case isCommand(command, "profileCfg"):
			if !modeSeen {
				return fmt.Errorf("dfeDataOutputMode must precede profileCfg, chirpCfg, and frameCfg in a full configuration: %s", command)
			}
			profileSeen = true
		case isCommand(command, "chirpCfg"):
			if !modeSeen {
				return fmt.Errorf("dfeDataOutputMode must precede profileCfg, chirpCfg, and frameCfg in a full configuration: %s", command)
			}
			if (hasChannel && !channelSeen) || (hasProfile && !profileSeen) {
				return fmt.Errorf("channelCfg and profileCfg must precede chirpCfg in a full configuration: %s", command)
			}
			if frameSeen {
				return fmt.Errorf("all chirpCfg commands must precede frameCfg in a full configuration: %s", command)
			}
			chirpSeen = true
		case isCommand(command, "frameCfg"):
			if !modeSeen {
				return fmt.Errorf("dfeDataOutputMode must precede profileCfg, chirpCfg, and frameCfg in a full configuration: %s", command)
			}
			if hasChirp && !chirpSeen {
				return fmt.Errorf("chirpCfg must precede frameCfg in a full configuration: %s", command)
			}
			frameSeen = true
		}
	}
	return nil
}

func validateADC(commands []string) (int, error) {
	count := 0
	for _, command := range commands {
		if !isCommand(command, "adcCfg") {
			continue
		}
		if err := requireExactName(command, "adcCfg"); err != nil {
			return 0, err
		}
		fields := strings.Fields(command)
		if len(fields) != 3 {
			return 0, fmt.Errorf("adcCfg must contain bit-width and output-format fields: %s", command)
		}
		bits, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, fmt.Errorf("invalid adcCfg bit-width in %q: %w", command, err)
		}
		format, err := strconv.Atoi(fields[2])
		if err != nil {
			return 0, fmt.Errorf("invalid adcCfg output format in %q: %w", command, err)
		}
		if bits != 2 || (format != 1 && format != 2) {
			return 0, fmt.Errorf("only 16-bit complex ADC (adcCfg 2 1 or adcCfg 2 2) is supported: %s", command)
		}
		count++
	}
	if count != 1 {
		return 0, errors.New("configuration must contain exactly one parseable adcCfg")
	}
	// TI's DCA1000 data-format code for 16-bit samples is adcBitsCode + 1.
	return 3, nil
}

func validateADCBuf(dialect Dialect, commands []string) error {
	count := 0
	for _, command := range commands {
		if !isCommand(command, "adcbufCfg") {
			continue
		}
		if err := requireExactName(command, "adcbufCfg"); err != nil {
			return err
		}
		fields := strings.Fields(command)
		if len(fields) != 6 {
			return fmt.Errorf("adcbufCfg must contain subframe, ADC format, IQ swap, channel interleave, and chirp threshold: %s", command)
		}
		values := make([]int, 5)
		for index := range values {
			value, err := strconv.Atoi(fields[index+1])
			if err != nil {
				return fmt.Errorf("cannot parse adcbufCfg %q: %w", command, err)
			}
			values[index] = value
		}
		if values[1] != 0 {
			return fmt.Errorf("capture requires complex ADCBuf format (adcFmt=0): %s", command)
		}
		if values[4] != 1 {
			return fmt.Errorf("xWR68xx CLI requires adcbufCfg chirpThreshold=1: %s", command)
		}
		if dialect == StudioCLI &&
			(values[0] != -1 || values[2] != 1 || values[3] != 1) {
			return fmt.Errorf("TI xWR68xx studio_cli firmware requires adcbufCfg -1 0 1 1 1: %s", command)
		}
		count++
	}
	if count != 1 {
		return errors.New("configuration must contain exactly one adcbufCfg compatible with complex raw capture")
	}
	return nil
}

func validateHardwareLVDS(commands []string) error {
	count := 0
	for _, command := range commands {
		if !isCommand(command, "lvdsStreamCfg") {
			continue
		}
		if err := requireExactName(command, "lvdsStreamCfg"); err != nil {
			return err
		}
		fields := strings.Fields(command)
		if len(fields) != 5 {
			return fmt.Errorf("cannot parse lvdsStreamCfg: %s", command)
		}
		values := make([]int, 4)
		for index := range values {
			value, err := strconv.Atoi(fields[index+1])
			if err != nil {
				return fmt.Errorf("cannot parse lvdsStreamCfg %q: %w", command, err)
			}
			values[index] = value
		}
		if values[0] != -1 || values[1] != 0 || values[2] != 1 || values[3] != 0 {
			return fmt.Errorf("capture requires lvdsStreamCfg -1 0 1 0 (no header, hardware ADC, software off): %s", command)
		}
		count++
	}
	if count != 1 {
		return errors.New("configuration must contain exactly one lvdsStreamCfg -1 0 1 0")
	}
	return nil
}

type frameConfiguration struct {
	chirpStart uint64
	chirpEnd   uint64
	loops      uint64
	frames     uint16
	period     time.Duration
}

type chirpProfileRange struct {
	start     uint64
	end       uint64
	profileID uint64
	txEnable  uint64
}

func parseFrame(commands []string) (frameConfiguration, error) {
	count := 0
	var result frameConfiguration
	for _, command := range commands {
		if !isCommand(command, "frameCfg") {
			continue
		}
		if err := requireExactName(command, "frameCfg"); err != nil {
			return frameConfiguration{}, err
		}
		fields := strings.Fields(command)
		if len(fields) != 8 {
			return frameConfiguration{}, fmt.Errorf("legacy frameCfg must contain seven arguments: %s", command)
		}
		chirpStart, err := parseUnsignedArgument(command, fields, 1, 16, "frame chirp start index")
		if err != nil {
			return frameConfiguration{}, err
		}
		chirpEnd, err := parseUnsignedArgument(command, fields, 2, 16, "frame chirp end index")
		if err != nil {
			return frameConfiguration{}, err
		}
		if chirpEnd < chirpStart {
			return frameConfiguration{}, fmt.Errorf("frameCfg chirp end index is before its start index: %s", command)
		}
		if chirpStart > xwr68xxMaximumChirpIndex || chirpEnd > xwr68xxMaximumChirpIndex {
			return frameConfiguration{}, fmt.Errorf("xWR68xx frameCfg chirp indices must be in 0..511: %s", command)
		}
		loops, err := parseUnsignedArgument(command, fields, 3, 16, "frame loop count")
		if err != nil {
			return frameConfiguration{}, err
		}
		if loops == 0 || loops > xwr68xxMaximumFrameLoops {
			return frameConfiguration{}, fmt.Errorf("xWR68xx frameCfg loop count must be in 1..255: %s", command)
		}
		frameValue, err := strconv.ParseUint(fields[4], 10, 16)
		if err != nil {
			return frameConfiguration{}, fmt.Errorf("invalid frame count in %q: %w", command, err)
		}
		milliseconds, err := strconv.ParseFloat(fields[5], 64)
		if err != nil || math.IsNaN(milliseconds) || math.IsInf(milliseconds, 0) || milliseconds <= 0 {
			return frameConfiguration{}, fmt.Errorf("invalid frame periodicity in %q", command)
		}
		if milliseconds > float64(math.MaxInt64)/float64(time.Millisecond) {
			return frameConfiguration{}, fmt.Errorf("frame periodicity exceeds supported duration: %s", command)
		}
		trigger, err := strconv.Atoi(fields[6])
		if err != nil {
			return frameConfiguration{}, fmt.Errorf("invalid frame trigger in %q: %w", command, err)
		}
		if trigger != 1 {
			return frameConfiguration{}, fmt.Errorf("only software-triggered frameCfg (triggerSelect=1) is supported: %s", command)
		}
		period := time.Duration(milliseconds * float64(time.Millisecond))
		if period < xwr68xxMinimumFramePeriod || period > xwr68xxMaximumFramePeriod {
			return frameConfiguration{}, fmt.Errorf("xWR68xx frame periodicity must be in 0.3..1342 ms: %s", command)
		}
		triggerDelay, err := strconv.ParseFloat(fields[7], 64)
		if err != nil || math.IsNaN(triggerDelay) || math.IsInf(triggerDelay, 0) || triggerDelay != 0 {
			return frameConfiguration{}, fmt.Errorf("initial xWR68xx single-chip capture requires frameTriggerDelay=0: %s", command)
		}
		result = frameConfiguration{
			chirpStart: chirpStart,
			chirpEnd:   chirpEnd,
			loops:      loops,
			frames:     uint16(frameValue),
			period:     period,
		}
		count++
	}
	if count != 1 {
		return frameConfiguration{}, errors.New("configuration must contain exactly one legacy frameCfg")
	}
	return result, nil
}

func deriveExpectedBytes(dialect Dialect, commands []string, frame frameConfiguration) (int64, int64, error) {
	receivers, enabledTransmitters, err := parseChannelConfiguration(commands)
	if err != nil {
		return 0, 0, err
	}
	profiles, err := parseProfileSamples(dialect, commands)
	if err != nil {
		return 0, 0, err
	}
	uniqueChirps, err := checkedExpectedAdd(frame.chirpEnd-frame.chirpStart, 1, "inclusive frame chirp range")
	if err != nil {
		return 0, 0, err
	}
	// Both supported xWR68xx text CLI firmware families use a static
	// 32-entry table while validating the unique chirps in a legacy frame.
	if uniqueChirps > 32 {
		return 0, 0, fmt.Errorf("xWR68xx text CLI supports at most 32 unique frame chirps, got %d", uniqueChirps)
	}
	if dialect == StudioCLI {
		// The studio_cli source audited from Radar Toolbox 4.00.00.05
		// hard-codes profile index 0 in mmw_rfparser.c. Reject configurations
		// the firmware cannot represent instead of deriving a byte count for a
		// different effective configuration.
		if len(profiles) != 1 {
			return 0, 0, errors.New("TI xWR68xx studio_cli firmware requires exactly one profileCfg for profile ID 0")
		}
		if _, found := profiles[0]; !found {
			return 0, 0, errors.New("TI xWR68xx studio_cli firmware requires its only profileCfg to use profile ID 0")
		}
	}
	ranges, err := parseChirpProfileRanges(commands, profiles, enabledTransmitters)
	if err != nil {
		return 0, 0, err
	}
	if dialect == StudioCLI && len(ranges) > 5 {
		return 0, 0, fmt.Errorf("TI xWR68xx studio_cli firmware stores at most five chirpCfg ranges, got %d", len(ranges))
	}
	samplesPerLoop, samplesPerChirp, err := mappedSamplesPerLoop(frame, profiles, ranges)
	if err != nil {
		return 0, 0, err
	}
	if err := validateRawBufferSizes(samplesPerChirp, receivers); err != nil {
		return 0, 0, err
	}

	samplesPerFrame, err := checkedExpectedMultiply(samplesPerLoop, frame.loops, "samples per loop by frame loop count")
	if err != nil {
		return 0, 0, err
	}
	samplesAcrossReceivers, err := checkedExpectedMultiply(samplesPerFrame, receivers, "samples per frame by enabled RX channels")
	if err != nil {
		return 0, 0, err
	}
	// Headerless CBUFF ADC streaming emits the configured complex16 samples
	// themselves. The ADCBuf's internal 16-byte channel alignment is not part
	// of the LVDS payload, so every RX/sample contributes exactly four bytes.
	bytesPerFrame, err := checkedExpectedMultiply(samplesAcrossReceivers, complex16BytesPerSample, "complex16 samples by four bytes")
	if err != nil {
		return 0, 0, err
	}
	bytesPerFrameSigned, err := checkedExpectedInt64(bytesPerFrame, "bytes per frame")
	if err != nil {
		return 0, 0, err
	}
	if frame.frames == 0 {
		return bytesPerFrameSigned, 0, nil
	}
	expected, err := checkedExpectedMultiply(bytesPerFrame, uint64(frame.frames), "bytes per frame by finite frame count")
	if err != nil {
		return 0, 0, err
	}
	expectedSigned, err := checkedExpectedInt64(expected, "finite capture bytes")
	if err != nil {
		return 0, 0, err
	}
	return bytesPerFrameSigned, expectedSigned, nil
}

func parseChannelConfiguration(commands []string) (uint64, uint64, error) {
	count := 0
	var mask uint64
	var transmitters uint64
	for _, command := range commands {
		if !isCommand(command, "channelCfg") {
			continue
		}
		if err := requireExactName(command, "channelCfg"); err != nil {
			return 0, 0, err
		}
		fields := strings.Fields(command)
		if len(fields) != 4 {
			return 0, 0, fmt.Errorf("channelCfg must contain RX mask, TX mask, and cascading fields: %s", command)
		}
		value, err := parseUnsignedArgument(command, fields, 1, 32, "channelCfg RX mask")
		if err != nil {
			return 0, 0, err
		}
		if value == 0 || value&^uint64(0x0f) != 0 {
			return 0, 0, fmt.Errorf("xWR68xx channelCfg RX mask must enable one or more of RX0..RX3: %s", command)
		}
		txMask, err := parseUnsignedArgument(command, fields, 2, 32, "channelCfg TX mask")
		if err != nil {
			return 0, 0, err
		}
		if txMask == 0 || txMask&^uint64(0x07) != 0 {
			return 0, 0, fmt.Errorf("xWR68xx channelCfg TX mask must enable one or more of TX0..TX2: %s", command)
		}
		cascade, err := parseUnsignedArgument(command, fields, 3, 32, "channelCfg cascading mode")
		if err != nil {
			return 0, 0, err
		}
		if cascade != 0 {
			return 0, 0, fmt.Errorf("initial xWR68xx capture supports only single-chip channelCfg cascading=0: %s", command)
		}
		mask = value
		transmitters = txMask
		count++
	}
	if count != 1 {
		return 0, 0, errors.New("configuration must contain exactly one unambiguous channelCfg")
	}
	return uint64(bits.OnesCount64(mask)), transmitters, nil
}

func parseProfileSamples(dialect Dialect, commands []string) (map[uint64]uint64, error) {
	profiles := make(map[uint64]uint64)
	for _, command := range commands {
		if !isCommand(command, "profileCfg") {
			continue
		}
		if err := requireExactName(command, "profileCfg"); err != nil {
			return nil, err
		}
		fields := strings.Fields(command)
		if len(fields) != 15 {
			return nil, fmt.Errorf("profileCfg must contain fourteen arguments: %s", command)
		}
		profileID, err := parseUnsignedArgument(command, fields, 1, 16, "profileCfg profile ID")
		if err != nil {
			return nil, err
		}
		if profileID >= 4 {
			return nil, fmt.Errorf("xWR68xx profileCfg profile ID must be in 0..3: %s", command)
		}
		if dialect == StudioCLI {
			frequencySlope, err := strconv.ParseFloat(fields[8], 64)
			if err != nil || math.IsNaN(frequencySlope) || math.IsInf(frequencySlope, 0) {
				return nil, fmt.Errorf("invalid profileCfg frequency slope in %q", command)
			}
			if frequencySlope < 0 {
				return nil, fmt.Errorf("TI xWR68xx studio_cli firmware does not support negative profileCfg frequency slope: %s", command)
			}
		}
		samples, err := parseUnsignedArgument(command, fields, 10, 16, "profileCfg numAdcSamples")
		if err != nil {
			return nil, err
		}
		if samples == 0 {
			return nil, fmt.Errorf("profileCfg numAdcSamples must be positive: %s", command)
		}
		if _, duplicate := profiles[profileID]; duplicate {
			return nil, fmt.Errorf("profileCfg profile ID %d is defined more than once", profileID)
		}
		profiles[profileID] = samples
	}
	if len(profiles) == 0 {
		return nil, errors.New("configuration must contain at least one parseable profileCfg")
	}
	return profiles, nil
}

func parseChirpProfileRanges(
	commands []string,
	profiles map[uint64]uint64,
	enabledTransmitters uint64,
) ([]chirpProfileRange, error) {
	var ranges []chirpProfileRange
	for _, command := range commands {
		if !isCommand(command, "chirpCfg") {
			continue
		}
		if err := requireExactName(command, "chirpCfg"); err != nil {
			return nil, err
		}
		fields := strings.Fields(command)
		if len(fields) != 9 {
			return nil, fmt.Errorf("chirpCfg must contain eight arguments: %s", command)
		}
		start, err := parseUnsignedArgument(command, fields, 1, 16, "chirpCfg start index")
		if err != nil {
			return nil, err
		}
		end, err := parseUnsignedArgument(command, fields, 2, 16, "chirpCfg end index")
		if err != nil {
			return nil, err
		}
		if end < start {
			return nil, fmt.Errorf("chirpCfg end index is before its start index: %s", command)
		}
		if start > xwr68xxMaximumChirpIndex || end > xwr68xxMaximumChirpIndex {
			return nil, fmt.Errorf("xWR68xx chirpCfg indices must be in 0..511: %s", command)
		}
		profileID, err := parseUnsignedArgument(command, fields, 3, 16, "chirpCfg profile ID")
		if err != nil {
			return nil, err
		}
		if profileID >= 4 {
			return nil, fmt.Errorf("xWR68xx chirpCfg profile ID must be in 0..3: %s", command)
		}
		if _, found := profiles[profileID]; !found {
			return nil, fmt.Errorf("chirpCfg range %d..%d references profile ID %d without a matching profileCfg", start, end, profileID)
		}
		txEnable, err := parseUnsignedArgument(command, fields, 8, 16, "chirpCfg TX enable mask")
		if err != nil {
			return nil, err
		}
		if txEnable&^uint64(0x07) != 0 {
			return nil, fmt.Errorf("xWR68xx chirpCfg TX enable mask must use TX0..TX2 only: %s", command)
		}
		if txEnable&^enabledTransmitters != 0 {
			return nil, fmt.Errorf("chirpCfg TX enable mask must be a subset of channelCfg TX mask: %s", command)
		}
		if bits.OnesCount64(txEnable) > 2 {
			return nil, fmt.Errorf("xWR68xx chirpCfg may enable at most two transmitters per chirp: %s", command)
		}
		ranges = append(ranges, chirpProfileRange{
			start:     start,
			end:       end,
			profileID: profileID,
			txEnable:  txEnable,
		})
	}
	if len(ranges) == 0 {
		return nil, errors.New("configuration must contain at least one parseable chirpCfg")
	}
	sort.Slice(ranges, func(left, right int) bool {
		if ranges[left].start == ranges[right].start {
			return ranges[left].end < ranges[right].end
		}
		return ranges[left].start < ranges[right].start
	})
	for index := 1; index < len(ranges); index++ {
		if ranges[index].start <= ranges[index-1].end {
			return nil, fmt.Errorf(
				"chirpCfg ranges overlap and make chirp-to-profile mapping ambiguous: %d..%d and %d..%d",
				ranges[index-1].start,
				ranges[index-1].end,
				ranges[index].start,
				ranges[index].end,
			)
		}
	}
	return ranges, nil
}

func mappedSamplesPerLoop(
	frame frameConfiguration,
	profiles map[uint64]uint64,
	ranges []chirpProfileRange,
) (uint64, uint64, error) {
	nextChirp := frame.chirpStart
	var frameProfileID uint64
	profileSelected := false
	var selectedTransmitters uint64
	for _, configured := range ranges {
		if configured.end < nextChirp {
			continue
		}
		if configured.start > frame.chirpEnd {
			break
		}
		if configured.start > nextChirp {
			return 0, 0, fmt.Errorf("frameCfg chirp index %d has no chirpCfg-to-profile mapping", nextChirp)
		}
		intersectionEnd := configured.end
		if intersectionEnd > frame.chirpEnd {
			intersectionEnd = frame.chirpEnd
		}
		if !profileSelected {
			frameProfileID = configured.profileID
			profileSelected = true
		} else if configured.profileID != frameProfileID {
			return 0, 0, fmt.Errorf(
				"frameCfg chirps use mixed profile IDs %d and %d; xWR68xx requires one profile per frame",
				frameProfileID,
				configured.profileID,
			)
		}
		selectedTransmitters |= configured.txEnable
		if intersectionEnd == frame.chirpEnd {
			if selectedTransmitters == 0 {
				return 0, 0, errors.New("frameCfg chirps must enable at least one channelCfg transmitter")
			}
			uniqueChirps, err := checkedExpectedAdd(frame.chirpEnd-frame.chirpStart, 1, "inclusive frame chirp range")
			if err != nil {
				return 0, 0, err
			}
			samplesPerChirp := profiles[frameProfileID]
			samplesPerLoop, err := checkedExpectedMultiply(
				uniqueChirps,
				samplesPerChirp,
				"unique frame chirps by profileCfg numAdcSamples",
			)
			return samplesPerLoop, samplesPerChirp, err
		}
		nextChirp = intersectionEnd + 1
	}
	return 0, 0, fmt.Errorf("frameCfg chirp index %d has no chirpCfg-to-profile mapping", nextChirp)
}

func validateRawBufferSizes(samplesPerChirp, receivers uint64) error {
	channelBytes, err := checkedExpectedMultiply(
		samplesPerChirp,
		complex16BytesPerSample,
		"profileCfg samples by complex16 bytes",
	)
	if err != nil {
		return err
	}
	if channelBytes > cbuffMaximumTransferSize {
		return fmt.Errorf(
			"xWR68xx CBUFF complex16 linked-list transfer is %d bytes per RX; at most %d bytes are supported",
			channelBytes,
			cbuffMaximumTransferSize,
		)
	}
	transferBytes, err := checkedExpectedMultiply(channelBytes, receivers, "CBUFF ADC bytes per chirp")
	if err != nil {
		return err
	}
	if transferBytes < cbuffMinimumTransferSize {
		return fmt.Errorf(
			"xWR68xx CBUFF ADC-only transfer is %d bytes per chirp; at least %d bytes are required",
			transferBytes,
			cbuffMinimumTransferSize,
		)
	}
	alignedInput, err := checkedExpectedAdd(channelBytes, adcBufChannelAlignment-1, "ADCBuf channel alignment")
	if err != nil {
		return err
	}
	alignedChannelBytes := alignedInput &^ (adcBufChannelAlignment - 1)
	adcBufBytes, err := checkedExpectedMultiply(alignedChannelBytes, receivers, "aligned ADCBuf channel bytes by enabled RX channels")
	if err != nil {
		return err
	}
	if adcBufBytes > xwr68xxADCBufBytes {
		return fmt.Errorf(
			"xWR68xx ADCBuf requires %d bytes (16-byte aligned per RX channel), exceeding its %d-byte capacity",
			adcBufBytes,
			xwr68xxADCBufBytes,
		)
	}
	return nil
}

func parseUnsignedArgument(command string, fields []string, index, bitSize int, name string) (uint64, error) {
	value, err := strconv.ParseUint(fields[index], 10, bitSize)
	if err != nil {
		return 0, fmt.Errorf("invalid %s in %q: %w", name, command, err)
	}
	return value, nil
}

func checkedExpectedMultiply(left, right uint64, description string) (uint64, error) {
	if left != 0 && right > ^uint64(0)/left {
		return 0, fmt.Errorf("expected byte derivation overflow (%s): %d * %d", description, left, right)
	}
	return left * right, nil
}

func checkedExpectedAdd(left, right uint64, description string) (uint64, error) {
	if right > ^uint64(0)-left {
		return 0, fmt.Errorf("expected byte derivation overflow (%s): %d + %d", description, left, right)
	}
	return left + right, nil
}

func checkedExpectedInt64(value uint64, description string) (int64, error) {
	if value > uint64(math.MaxInt64) {
		return 0, fmt.Errorf("expected %s exceed supported int64 file size: %d", description, value)
	}
	return int64(value), nil
}

func parseIntField(fields []string, index, expectedFields int) (int, error) {
	if len(fields) != expectedFields {
		return 0, fmt.Errorf("expected %d fields, got %d", expectedFields, len(fields))
	}
	value, err := strconv.Atoi(fields[index])
	if err != nil {
		return 0, err
	}
	return value, nil
}

func isCommand(command, expected string) bool {
	fields := strings.Fields(command)
	return len(fields) != 0 && strings.EqualFold(fields[0], expected)
}

func requireExactName(command, expected string) error {
	fields := strings.Fields(command)
	if len(fields) == 0 || fields[0] != expected {
		return fmt.Errorf("xWR68xx CLI command names are case-sensitive; use %s: %s", expected, command)
	}
	return nil
}

func frameSpan(frames uint16, period time.Duration) (time.Duration, error) {
	if frames == 0 || frames == 1 {
		return 0, nil
	}
	multiplier := int64(frames - 1)
	if period > time.Duration(math.MaxInt64/multiplier) {
		return 0, errors.New("frameCfg-derived finite-frame duration exceeds supported range")
	}
	return time.Duration(multiplier) * period, nil
}

// ExpectedFrameSpan returns (numberOfFrames-1)*period for a finite capture.
// The boolean is false for an infinite-frame plan.
func (p CapturePlan) ExpectedFrameSpan() (time.Duration, bool) {
	if p.InfiniteFrames {
		return 0, false
	}
	span, err := frameSpan(p.NumberOfFrames, p.FramePeriod)
	return span, err == nil
}

// MaximumStreamingDuration supplies a finite absolute deadline after the first
// data packet. Two idle windows cover a delayed final short payload and the
// post-target quiet verification that rejects an overlong stream.
func (p CapturePlan) MaximumStreamingDuration(idle time.Duration) (time.Duration, bool, error) {
	span, finite := p.ExpectedFrameSpan()
	if !finite {
		return 0, false, nil
	}
	if idle < 0 {
		return 0, true, errors.New("idle duration must not be negative")
	}
	if p.FramePeriod < 0 || p.FramePeriod > time.Duration(math.MaxInt64/2) {
		return 0, true, errors.New("frame period exceeds supported range")
	}
	guard := max(2*p.FramePeriod, time.Second)
	if idle > time.Duration(math.MaxInt64/2) {
		return 0, true, errors.New("idle duration is too large to derive a finite maximum")
	}
	idleWindows := 2 * idle
	if span > time.Duration(math.MaxInt64)-idleWindows || span+idleWindows > time.Duration(math.MaxInt64)-guard {
		return 0, true, errors.New("frameCfg-derived maximum duration exceeds supported range")
	}
	return span + idleWindows + guard, true, nil
}
