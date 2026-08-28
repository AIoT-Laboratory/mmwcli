package iwr6843

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"reflect"
	"strconv"
	"strings"

	"mmwcli/internal/radar"
)

const (
	mmWaveLinkRFStaticConfigMessageID  = 0x004
	mmWaveLinkRFInitMessageID          = 0x006
	mmWaveLinkRFDynamicConfigMessageID = 0x008
	mmWaveLinkRFMiscConfigMessageID    = 0x016
	mmWaveLinkDeviceConfigMessageID    = 0x202
	mmWaveLinkDeviceApplyMessageID     = 0x206

	mmWaveLinkRFChannelSubblockID        = 0
	mmWaveLinkRFADCSubblockID            = 2
	mmWaveLinkRFLowPowerSubblockID       = 3
	mmWaveLinkRFHSIClockSubblockID       = 5
	mmWaveLinkRFInitSubblockID           = 0
	mmWaveLinkRFProfileSubblockID        = 0
	mmWaveLinkRFChirpSubblockID          = 1
	mmWaveLinkRFFrameSubblockID          = 2
	mmWaveLinkRFTestSourceEnableID       = 3
	mmWaveLinkDeviceDataFormatSubblockID = 1
	mmWaveLinkDeviceDataPathSubblockID   = 2
	mmWaveLinkDeviceLaneEnableSubblockID = 3
	mmWaveLinkDeviceClockSubblockID      = 4
	mmWaveLinkDeviceLVDSSubblockID       = 5
	mmWaveLinkDeviceFrameApplySubblockID = 0

	mmWaveLinkRFInitEventSubblockID = 4
	mmWaveLinkRFInitEventDataLength = 20
	mmWaveLinkRFInitSuccessMask     = uint32(0x1ffe)

	xwr6843FrequencyScale = 2.7
	xwr6843StartMinimum   = uint64(0x5471c71c)
	xwr6843StartMaximum   = uint64(0x5ed097b4)
)

type debugRFEncodingContract struct {
	label                    string
	frequencyScale           float64
	startMinimum             uint64
	startMaximum             uint64
	slopeMaximum             int64
	requireEven              bool
	powerBackoffMaximum      byte
	rxGainMinimum            uint64
	rxGainMaximum            uint64
	reservedRFGainTarget     uint64
	sampleRateMaximum        uint64
	complex1XSampleRateLimit uint16
}

func iwr6843RFEncoding() debugRFEncodingContract {
	return debugRFEncodingContract{
		label:                    "xWR6843",
		frequencyScale:           xwr6843FrequencyScale,
		startMinimum:             xwr6843StartMinimum,
		startMaximum:             xwr6843StartMaximum,
		slopeMaximum:             6905,
		requireEven:              true,
		powerBackoffMaximum:      26,
		rxGainMinimum:            30,
		rxGainMaximum:            48,
		reservedRFGainTarget:     3,
		sampleRateMaximum:        25000,
		complex1XSampleRateLimit: 12500,
	}
}

// Plan is an immutable, offline translation of a validated legacy capture
// plan into the mmWaveLink operations required by IWR6843.
// Its contents are intentionally opaque outside this package.
type Plan struct {
	source     radar.Plan
	operations []mmWaveLinkPlanOperation
}

type mmWaveLinkPlanOperation struct {
	command mmWaveLinkCommand
	await   *mmWaveLinkPlanEvent
}

type mmWaveLinkPlanEvent struct {
	direction       rhcpDirection
	messageID       uint16
	subblockID      uint16
	dataLength      int
	calibrationMask uint32
}

type mmWaveLinkConfiguration struct {
	rxMask     uint16
	txMask     uint16
	adcBits    uint16
	adcFormat  uint16
	iqSwap     uint8
	interleave uint8
	profile    mmWaveLinkProfileConfiguration
	chirps     []mmWaveLinkChirpConfiguration
	frame      mmWaveLinkFrameConfiguration
}

type mmWaveLinkProfileConfiguration struct {
	startFrequency uint32
	idleTime       uint32
	adcStartTime   uint32
	rampEndTime    uint32
	powerBackoff   uint32
	phaseShifter   uint32
	frequencySlope int16
	txStartTime    int16
	samples        uint16
	sampleRate     uint16
	hpf1           uint8
	hpf2           uint8
	rxGain         uint16
}

type mmWaveLinkChirpConfiguration struct {
	start          uint16
	end            uint16
	profileID      uint16
	startVariation uint32
	slopeVariation uint16
	idleVariation  uint16
	adcVariation   uint16
	txMask         uint16
}

type mmWaveLinkFrameConfiguration struct {
	chirpStart uint16
	chirpEnd   uint16
	loops      uint16
	frames     uint16
	period     uint32
}

// BuildPlan performs the complete IWR6843 CFG-to-wire preflight offline.
func BuildPlan(source radar.Plan) (Plan, error) {
	return buildPlan(source)
}

func buildPlan(source radar.Plan) (Plan, error) {
	snapshot := clonePlan(source)
	if err := validatePlan(snapshot); err != nil {
		return Plan{}, err
	}

	configuration, err := parseMMWaveLinkConfiguration(snapshot.ConfigurationCommands)
	if err != nil {
		return Plan{}, err
	}
	operations, err := buildMMWaveLinkOperations(configuration)
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		source:     snapshot,
		operations: operations,
	}, nil
}

func validatePlan(source radar.Plan) error {
	commands := append([]string(nil), source.ConfigurationCommands...)
	if source.DeclaredStartCommand != "" {
		commands = append(commands, source.DeclaredStartCommand)
	}
	rebuilt, err := radar.CommandPlan(commands)
	if err != nil {
		return fmt.Errorf("invalid IWR6843 source plan: %w", err)
	}
	if !reflect.DeepEqual(rebuilt, source) {
		return errors.New("IWR6843 source plan metadata does not match its configuration commands")
	}
	return nil
}

func clonePlan(source radar.Plan) radar.Plan {
	result := source
	result.ConfigurationCommands = append([]string(nil), source.ConfigurationCommands...)
	return result
}

func (plan Plan) matches(source radar.Plan) bool {
	return reflect.DeepEqual(plan.source, clonePlan(source))
}

func (plan Plan) operationsCopy() []mmWaveLinkPlanOperation {
	result := make([]mmWaveLinkPlanOperation, len(plan.operations))
	for index, operation := range plan.operations {
		result[index].command = cloneMMWaveLinkCommand(operation.command)
		if operation.await != nil {
			expected := *operation.await
			result[index].await = &expected
		}
	}
	return result
}

func cloneMMWaveLinkCommand(command mmWaveLinkCommand) mmWaveLinkCommand {
	result := command
	result.subblocks = make([]mmWaveLinkSubblock, len(command.subblocks))
	for index, subblock := range command.subblocks {
		result.subblocks[index] = mmWaveLinkSubblock{
			id:   subblock.id,
			data: append([]byte(nil), subblock.data...),
		}
	}
	return result
}

func (expected mmWaveLinkPlanEvent) validate(message mmWaveLinkMessage) error {
	if message.direction != expected.direction ||
		message.messageClass != rhcpMessageClassAsync ||
		message.messageID != expected.messageID ||
		len(message.subblocks) != 1 ||
		message.subblocks[0].id != expected.subblockID ||
		len(message.subblocks[0].data) != expected.dataLength {
		return fmt.Errorf(
			"unexpected mmWaveLink event; want direction %d message %#03x sub-block %#02x with %d data bytes",
			expected.direction,
			expected.messageID,
			expected.subblockID,
			expected.dataLength,
		)
	}
	if expected.calibrationMask != 0 {
		status := binary.LittleEndian.Uint32(message.subblocks[0].data[:4])
		if status&expected.calibrationMask != expected.calibrationMask {
			return fmt.Errorf(
				"RF initialization calibration status %#08x does not contain required mask %#08x",
				status,
				expected.calibrationMask,
			)
		}
	}
	return nil
}

func parseMMWaveLinkConfiguration(commands []string) (mmWaveLinkConfiguration, error) {
	var result mmWaveLinkConfiguration
	encoding := iwr6843RFEncoding()
	counts := make(map[string]int)
	for _, command := range commands {
		fields := strings.Fields(command)
		if len(fields) == 0 {
			return result, errors.New("IWR6843 configuration contains an empty command")
		}
		name := fields[0]
		switch name {
		case "flushCfg":
			counts[name]++
			if len(fields) != 1 {
				return result, fmt.Errorf("IWR6843 capture requires exact flushCfg: %s", command)
			}
		case "dfeDataOutputMode":
			counts[name]++
			if !fieldsEqual(fields, "dfeDataOutputMode", "1") {
				return result, fmt.Errorf("IWR6843 capture requires exact dfeDataOutputMode 1: %s", command)
			}
		case "channelCfg":
			counts[name]++
			rx, tx, err := parseMMWaveLinkChannel(fields, command)
			if err != nil {
				return result, err
			}
			result.rxMask, result.txMask = rx, tx
		case "adcCfg":
			counts[name]++
			bits, format, err := parseMMWaveLinkADC(fields, command)
			if err != nil {
				return result, err
			}
			result.adcBits, result.adcFormat = bits, format
		case "adcbufCfg":
			counts[name]++
			iqSwap, interleave, err := parseMMWaveLinkADCBuf(fields, command)
			if err != nil {
				return result, err
			}
			result.iqSwap, result.interleave = iqSwap, interleave
		case "profileCfg":
			counts[name]++
			profile, err := parseMMWaveLinkProfile(fields, command, encoding)
			if err != nil {
				return result, err
			}
			result.profile = profile
		case "chirpCfg":
			chirp, err := parseMMWaveLinkChirp(fields, command, encoding)
			if err != nil {
				return result, err
			}
			result.chirps = append(result.chirps, chirp)
		case "frameCfg":
			counts[name]++
			frame, err := parseMMWaveLinkFrame(fields, command)
			if err != nil {
				return result, err
			}
			result.frame = frame
		case "lowPower":
			counts[name]++
			lowPowerMode := strconv.FormatUint(uint64(iwr6843LowPowerADCMode), 10)
			if !fieldsEqual(fields, "lowPower", "0", lowPowerMode) {
				return result, fmt.Errorf("IWR6843 capture requires exact lowPower 0 %s: %s", lowPowerMode, command)
			}
		case "lvdsStreamCfg":
			counts[name]++
			if !fieldsEqual(fields, "lvdsStreamCfg", "-1", "0", "1", "0") {
				return result, fmt.Errorf("IWR6843 capture requires exact lvdsStreamCfg -1 0 1 0: %s", command)
			}
		default:
			return result, fmt.Errorf("command %q is not part of the IWR6843 configuration contract", name)
		}
	}

	for _, name := range []string{
		"flushCfg",
		"dfeDataOutputMode",
		"channelCfg",
		"adcCfg",
		"adcbufCfg",
		"profileCfg",
		"frameCfg",
		"lowPower",
		"lvdsStreamCfg",
	} {
		if counts[name] != 1 {
			return result, fmt.Errorf("IWR6843 capture requires exactly one %s command, got %d", name, counts[name])
		}
	}
	if len(result.chirps) < 1 || len(result.chirps) > 5 {
		return result, fmt.Errorf("IWR6843 capture requires 1..5 chirpCfg commands, got %d", len(result.chirps))
	}
	if result.adcFormat == 1 && result.profile.sampleRate > encoding.complex1XSampleRateLimit {
		return result, fmt.Errorf(
			"%s regular complex1x ADC sample rate must be in 2000..%d ksps, got %d",
			iwr6843Platform,
			encoding.complex1XSampleRateLimit,
			result.profile.sampleRate,
		)
	}
	if err := validateMMWaveLinkRelationships(result); err != nil {
		return result, err
	}
	return result, nil
}

func validateMMWaveLinkRelationships(configuration mmWaveLinkConfiguration) error {
	covered := make([]bool, 512)
	for _, chirp := range configuration.chirps {
		if chirp.profileID != 0 {
			return fmt.Errorf("chirpCfg range %d..%d references unsupported profile ID %d", chirp.start, chirp.end, chirp.profileID)
		}
		if chirp.txMask == 0 {
			return fmt.Errorf("chirpCfg range %d..%d must enable at least one transmitter", chirp.start, chirp.end)
		}
		if chirp.txMask&^configuration.txMask != 0 {
			return fmt.Errorf(
				"chirpCfg range %d..%d TX mask %#x is not a subset of channelCfg TX mask %#x",
				chirp.start,
				chirp.end,
				chirp.txMask,
				configuration.txMask,
			)
		}
		if bits.OnesCount16(chirp.txMask) > int(iwr6843MaxChirpTransmitters) {
			return fmt.Errorf("chirpCfg range %d..%d enables more than two transmitters", chirp.start, chirp.end)
		}
		for index := chirp.start; index <= chirp.end; index++ {
			if covered[index] {
				return fmt.Errorf("chirpCfg index %d is defined by overlapping ranges", index)
			}
			covered[index] = true
		}
	}
	for index := configuration.frame.chirpStart; index <= configuration.frame.chirpEnd; index++ {
		if !covered[index] {
			return fmt.Errorf("frameCfg chirp index %d has no chirpCfg definition", index)
		}
	}
	return nil
}

func fieldsEqual(fields []string, expected ...string) bool {
	if len(fields) != len(expected) {
		return false
	}
	for index := range fields {
		if fields[index] != expected[index] {
			return false
		}
	}
	return true
}

func parseMMWaveLinkChannel(
	fields []string,
	command string,
) (uint16, uint16, error) {
	if len(fields) != 4 {
		return 0, 0, fmt.Errorf("channelCfg must contain RX mask, TX mask, and cascading mode: %s", command)
	}
	rx, err := parseUnsigned(fields[1], 0x0f, "channelCfg RX mask")
	if err != nil || rx == 0 {
		return 0, 0, invalidOrRange(err, "channelCfg RX mask", "1..15", command)
	}
	tx, err := parseUnsigned(fields[2], uint64(iwr6843TXMask), "channelCfg TX mask")
	if err != nil || tx == 0 {
		return 0, 0, invalidOrRange(
			err,
			"channelCfg TX mask",
			fmt.Sprintf("1..%d", iwr6843TXMask),
			command,
		)
	}
	cascade, err := parseUnsigned(fields[3], 0, "channelCfg cascading mode")
	if err != nil || cascade != 0 {
		return 0, 0, invalidOrRange(err, "channelCfg cascading mode", "0", command)
	}
	return uint16(rx), uint16(tx), nil
}

func parseMMWaveLinkADC(fields []string, command string) (uint16, uint16, error) {
	if len(fields) != 3 {
		return 0, 0, fmt.Errorf("adcCfg must contain bit-width and format: %s", command)
	}
	bits, err := parseUnsigned(fields[1], 2, "adcCfg bit-width")
	if err != nil || bits != 2 {
		return 0, 0, invalidOrRange(err, "adcCfg bit-width", "2", command)
	}
	format, err := parseUnsigned(fields[2], 2, "adcCfg output format")
	if err != nil || (format != 1 && format != 2) {
		return 0, 0, invalidOrRange(err, "adcCfg output format", "1 or 2", command)
	}
	return uint16(bits), uint16(format), nil
}

func parseMMWaveLinkADCBuf(fields []string, command string) (uint8, uint8, error) {
	if !fieldsEqual(fields, "adcbufCfg", "-1", "0", "1", "1", "1") {
		return 0, 0, fmt.Errorf("IWR6843 capture requires exact adcbufCfg -1 0 1 1 1: %s", command)
	}
	return 1, 1, nil
}

func parseMMWaveLinkProfile(
	fields []string,
	command string,
	encoding debugRFEncodingContract,
) (mmWaveLinkProfileConfiguration, error) {
	var result mmWaveLinkProfileConfiguration
	if len(fields) != 15 {
		return result, fmt.Errorf("profileCfg must contain fourteen arguments: %s", command)
	}
	profileID, err := parseUnsigned(fields[1], 0, "profileCfg profile ID")
	if err != nil || profileID != 0 {
		return result, invalidOrRange(err, "profileCfg profile ID", "0", command)
	}

	start, err := parseScaledUnsigned(fields[2], float64(uint64(1)<<26)/encoding.frequencyScale, math.MaxUint32, false, "profileCfg start frequency")
	if err != nil {
		return result, err
	}
	if start < encoding.startMinimum || start > encoding.startMaximum {
		return result, fmt.Errorf("%s profileCfg converted start frequency %#x is outside %#x..%#x", encoding.label, start, encoding.startMinimum, encoding.startMaximum)
	}
	if encoding.requireEven && start&1 != 0 {
		return result, fmt.Errorf("%s profileCfg converted start frequency %#x must be even", encoding.label, start)
	}
	result.startFrequency = uint32(start)

	if result.idleTime, err = parseStudioFloatTimeUnsigned(fields[3], 524287, "profileCfg idle time"); err != nil {
		return result, err
	}
	if result.adcStartTime, err = parseStudioFloatTimeUnsigned(fields[4], 4095, "profileCfg ADC start time"); err != nil {
		return result, err
	}
	if result.rampEndTime, err = parseStudioFloatTimeUnsigned(fields[5], 500000, "profileCfg ramp end time"); err != nil {
		return result, err
	}

	powerBackoff, err := parseUnsigned(fields[6], math.MaxUint32, "profileCfg TX power backoff")
	if err != nil {
		return result, err
	}
	if powerBackoff>>24 != 0 ||
		byte(powerBackoff) > encoding.powerBackoffMaximum ||
		byte(powerBackoff>>8) > encoding.powerBackoffMaximum ||
		byte(powerBackoff>>16) > encoding.powerBackoffMaximum {
		return result, fmt.Errorf(
			"profileCfg TX power backoff %#x has reserved bits or a per-TX code above %d",
			powerBackoff,
			encoding.powerBackoffMaximum,
		)
	}
	result.powerBackoff = uint32(powerBackoff)

	phase, err := parseUnsigned(fields[7], math.MaxUint32, "profileCfg TX phase shifter")
	if err != nil {
		return result, err
	}
	if phase>>24 != 0 || byte(phase)&3 != 0 || byte(phase>>8)&3 != 0 || byte(phase>>16)&3 != 0 {
		return result, fmt.Errorf("profileCfg TX phase shifter %#x sets reserved bits", phase)
	}
	result.phaseShifter = uint32(phase)

	slope, err := parseScaledSigned(
		fields[8],
		float64(uint64(1)<<26)/(encoding.frequencyScale*1000*900),
		0,
		encoding.slopeMaximum,
		false,
		"profileCfg frequency slope",
	)
	if err != nil {
		return result, err
	}
	// mmWave Studio 2.1.1's xWR68xx reference script converts its
	// 60.012 MHz/us slope to code 1657 and submits that value directly.
	// The start-frequency parity rule therefore does not apply here.
	result.frequencySlope = int16(slope)

	txStart, err := parseStudioFloatTimeSigned(fields[9], -4096, 4095, "profileCfg TX start time")
	if err != nil {
		return result, err
	}
	result.txStartTime = int16(txStart)

	samples, err := parseUnsigned(fields[10], math.MaxUint16, "profileCfg ADC sample count")
	if err != nil || samples < 2 {
		return result, invalidOrRange(err, "profileCfg ADC sample count", "2..65535", command)
	}
	if samples > math.MaxUint16/2 {
		return result, fmt.Errorf("profileCfg ADC sample count %d overflows the complex frame sample field", samples)
	}
	result.samples = uint16(samples)

	rate, err := parseUnsigned(fields[11], encoding.sampleRateMaximum, "profileCfg ADC sample rate")
	if err != nil || rate < 2000 {
		return result, invalidOrRange(
			err,
			"profileCfg ADC sample rate",
			fmt.Sprintf("2000..%d", encoding.sampleRateMaximum),
			command,
		)
	}
	result.sampleRate = uint16(rate)

	hpf1, err := parseUnsigned(fields[12], 3, "profileCfg HPF1")
	if err != nil {
		return result, err
	}
	hpf2, err := parseUnsigned(fields[13], 3, "profileCfg HPF2")
	if err != nil {
		return result, err
	}
	result.hpf1, result.hpf2 = uint8(hpf1), uint8(hpf2)

	rxGain, err := parseUnsigned(fields[14], math.MaxUint16, "profileCfg RX gain")
	if err != nil {
		return result, err
	}
	baseGain := rxGain & 0x3f
	rfTarget := (rxGain >> 6) & 0x03
	if rxGain>>8 != 0 || baseGain < encoding.rxGainMinimum || baseGain > encoding.rxGainMaximum ||
		baseGain&1 != 0 || rfTarget == encoding.reservedRFGainTarget {
		return result, fmt.Errorf("profileCfg RX gain %#x is invalid for %s", rxGain, iwr6843Identity)
	}
	result.rxGain = uint16(rxGain)
	return result, nil
}

func parseStudioFloatTimeUnsigned(value string, maximum uint64, name string) (uint32, error) {
	converted, err := parseFloat32ArithmeticUnsigned(value, 1000, 10, maximum, name)
	return uint32(converted), err
}

func parseStudioFloatTimeSigned(value string, minimum, maximum int64, name string) (int64, error) {
	parsed, err := parseFiniteFloat(value, name)
	if err != nil {
		return 0, err
	}
	input := float32(parsed)
	converted := float32(input*1000) / 10
	if math.IsInf(float64(converted), 0) {
		return 0, fmt.Errorf("%s %q overflows studio_cli float conversion", name, value)
	}
	truncated := math.Trunc(float64(converted))
	if truncated < float64(minimum) || truncated > float64(maximum) {
		return 0, fmt.Errorf("%s %q overflows converted range %d..%d", name, value, minimum, maximum)
	}
	return int64(truncated), nil
}

func parseMMWaveLinkChirp(
	fields []string,
	command string,
	encoding debugRFEncodingContract,
) (mmWaveLinkChirpConfiguration, error) {
	var result mmWaveLinkChirpConfiguration
	if len(fields) != 9 {
		return result, fmt.Errorf("chirpCfg must contain eight arguments: %s", command)
	}
	start, err := parseUnsigned(fields[1], 511, "chirpCfg start index")
	if err != nil {
		return result, err
	}
	end, err := parseUnsigned(fields[2], 511, "chirpCfg end index")
	if err != nil {
		return result, err
	}
	if end < start {
		return result, fmt.Errorf("chirpCfg end index %d precedes start index %d", end, start)
	}
	profileID, err := parseUnsigned(fields[3], 0, "chirpCfg profile ID")
	if err != nil || profileID != 0 {
		return result, invalidOrRange(err, "chirpCfg profile ID", "0", command)
	}
	startVariation, err := parseFloat32ProductUnsigned(
		fields[4],
		float32(uint64(1)<<26),
		encoding.frequencyScale*1e9,
		8388607,
		"chirpCfg start frequency variation",
	)
	if err != nil {
		return result, err
	}
	if encoding.requireEven && startVariation&1 != 0 {
		return result, fmt.Errorf("%s chirpCfg converted start frequency variation %d must be even", encoding.label, startVariation)
	}
	slopeVariation, err := parseFloat32ProductUnsigned(
		fields[5],
		float32(uint64(1)<<26),
		encoding.frequencyScale*1e6*900,
		63,
		"chirpCfg slope variation",
	)
	if err != nil {
		return result, err
	}
	if encoding.requireEven && slopeVariation&1 != 0 {
		return result, fmt.Errorf("%s chirpCfg converted slope variation %d must be even", encoding.label, slopeVariation)
	}
	idle, err := parseScaledUnsigned(fields[6], 100, 4096, true, "chirpCfg idle variation")
	if err != nil {
		return result, err
	}
	adc, err := parseScaledUnsigned(fields[7], 100, 4096, true, "chirpCfg ADC start variation")
	if err != nil {
		return result, err
	}
	tx, err := parseUnsigned(fields[8], uint64(iwr6843TXMask), "chirpCfg TX mask")
	if err != nil {
		return result, err
	}
	result = mmWaveLinkChirpConfiguration{
		start:          uint16(start),
		end:            uint16(end),
		profileID:      uint16(profileID),
		startVariation: uint32(startVariation),
		slopeVariation: uint16(slopeVariation),
		idleVariation:  uint16(idle),
		adcVariation:   uint16(adc),
		txMask:         uint16(tx),
	}
	return result, nil
}

func parseMMWaveLinkFrame(
	fields []string,
	command string,
) (mmWaveLinkFrameConfiguration, error) {
	var result mmWaveLinkFrameConfiguration
	if len(fields) != 8 {
		return result, fmt.Errorf("frameCfg must contain seven arguments: %s", command)
	}
	start, err := parseUnsigned(fields[1], 511, "frameCfg chirp start index")
	if err != nil {
		return result, err
	}
	end, err := parseUnsigned(fields[2], 511, "frameCfg chirp end index")
	if err != nil {
		return result, err
	}
	if end < start {
		return result, fmt.Errorf("frameCfg end index %d precedes start index %d", end, start)
	}
	loops, err := parseUnsigned(fields[3], 255, "frameCfg loop count")
	if err != nil || loops == 0 {
		return result, invalidOrRange(err, "frameCfg loop count", "1..255", command)
	}
	frames, err := parseUnsigned(fields[4], math.MaxUint16, "frameCfg frame count")
	if err != nil {
		return result, err
	}
	period, err := parseFloat32ArithmeticUnsigned(fields[5], 1000000, 5, math.MaxUint32, "frameCfg period")
	if err != nil {
		return result, err
	}
	if period < 60000 || period > 268400000 {
		return result, fmt.Errorf("%s frameCfg converted period %d is outside 60000..268400000", iwr6843Platform, period)
	}
	trigger, err := parseUnsigned(fields[6], 1, "frameCfg trigger")
	if err != nil || trigger != 1 {
		return result, invalidOrRange(err, "frameCfg trigger", "1", command)
	}
	delay, err := parseFloat32ArithmeticUnsigned(fields[7], 1000000, 5, math.MaxUint32, "frameCfg trigger delay")
	if err != nil || delay != 0 {
		return result, invalidOrRange(err, "frameCfg trigger delay", "0", command)
	}
	return mmWaveLinkFrameConfiguration{
		chirpStart: uint16(start),
		chirpEnd:   uint16(end),
		loops:      uint16(loops),
		frames:     uint16(frames),
		period:     uint32(period),
	}, nil
}

func parseUnsigned(value string, maximum uint64, name string) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", name, value, err)
	}
	if parsed > maximum {
		return 0, fmt.Errorf("%s %d exceeds %d", name, parsed, maximum)
	}
	return parsed, nil
}

func parseScaledUnsigned(value string, scale float64, maximum uint64, useFloat32 bool, name string) (uint64, error) {
	parsed, err := parseFiniteFloat(value, name)
	if err != nil {
		return 0, err
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%s must not be negative: %q", name, value)
	}
	if useFloat32 {
		parsed = float64(float32(parsed))
		if math.IsInf(parsed, 0) {
			return 0, fmt.Errorf("%s %q overflows studio_cli float conversion", name, value)
		}
	}
	converted := math.Trunc(parsed * scale)
	if math.IsNaN(converted) || math.IsInf(converted, 0) || converted > float64(maximum) {
		return 0, fmt.Errorf("%s %q overflows converted range 0..%d", name, value, maximum)
	}
	return uint64(converted), nil
}

func parseFloat32ProductUnsigned(value string, multiplier float32, divisor float64, maximum uint64, name string) (uint64, error) {
	parsed, err := parseFiniteFloat(value, name)
	if err != nil {
		return 0, err
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%s must not be negative: %q", name, value)
	}
	input := float32(parsed)
	product := input * multiplier
	if math.IsInf(float64(product), 0) {
		return 0, fmt.Errorf("%s %q overflows studio_cli float conversion", name, value)
	}
	converted := math.Trunc(float64(product) / divisor)
	if math.IsNaN(converted) || math.IsInf(converted, 0) || converted > float64(maximum) {
		return 0, fmt.Errorf("%s %q overflows converted range 0..%d", name, value, maximum)
	}
	return uint64(converted), nil
}

func parseFloat32ArithmeticUnsigned(value string, multiplier, divisor float32, maximum uint64, name string) (uint64, error) {
	parsed, err := parseFiniteFloat(value, name)
	if err != nil {
		return 0, err
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%s must not be negative: %q", name, value)
	}
	input := float32(parsed)
	converted := input * multiplier / divisor
	if math.IsInf(float64(converted), 0) {
		return 0, fmt.Errorf("%s %q overflows studio_cli float conversion", name, value)
	}
	truncated := math.Trunc(float64(converted))
	if truncated > float64(maximum) {
		return 0, fmt.Errorf("%s %q overflows converted range 0..%d", name, value, maximum)
	}
	return uint64(truncated), nil
}

func parseScaledSigned(value string, scale float64, minimum, maximum int64, useFloat32 bool, name string) (int64, error) {
	parsed, err := parseFiniteFloat(value, name)
	if err != nil {
		return 0, err
	}
	if useFloat32 {
		parsed = float64(float32(parsed))
		if math.IsInf(parsed, 0) {
			return 0, fmt.Errorf("%s %q overflows studio_cli float conversion", name, value)
		}
	}
	converted := math.Trunc(parsed * scale)
	if math.IsNaN(converted) || math.IsInf(converted, 0) || converted < float64(minimum) || converted > float64(maximum) {
		return 0, fmt.Errorf("%s %q overflows converted range %d..%d", name, value, minimum, maximum)
	}
	return int64(converted), nil
}

func parseFiniteFloat(value, name string) (float64, error) {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, fmt.Errorf("invalid finite %s %q", name, value)
	}
	return parsed, nil
}

func invalidOrRange(parseError error, name, expected, command string) error {
	if parseError != nil {
		return parseError
	}
	return fmt.Errorf("%s must be %s: %s", name, expected, command)
}

func buildMMWaveLinkOperations(configuration mmWaveLinkConfiguration) ([]mmWaveLinkPlanOperation, error) {
	laneEnable := laneEnablePayload()
	operations := make([]mmWaveLinkPlanOperation, 0, 14+len(configuration.chirps))
	appendOperation := func(direction rhcpDirection, messageID, subblockID uint16, data []byte) {
		operations = append(operations, mmWaveLinkPlanOperation{command: mmWaveLinkCommand{
			direction: direction,
			messageID: messageID,
			subblocks: []mmWaveLinkSubblock{{id: subblockID, data: append([]byte(nil), data...)}},
		}})
	}

	appendOperation(rhcpDirectionHostToBSS, mmWaveLinkRFStaticConfigMessageID, mmWaveLinkRFChannelSubblockID, encodeChannel(configuration))
	appendOperation(rhcpDirectionHostToBSS, mmWaveLinkRFStaticConfigMessageID, mmWaveLinkRFADCSubblockID, encodeADC(configuration))
	appendOperation(rhcpDirectionHostToMSS, mmWaveLinkDeviceConfigMessageID, mmWaveLinkDeviceDataFormatSubblockID, encodeDataFormat(configuration))
	lowPower := make([]byte, 4)
	binary.LittleEndian.PutUint16(lowPower[2:4], iwr6843LowPowerADCMode)
	appendOperation(rhcpDirectionHostToBSS, mmWaveLinkRFStaticConfigMessageID, mmWaveLinkRFLowPowerSubblockID, lowPower)

	appendOperation(rhcpDirectionHostToBSS, mmWaveLinkRFInitMessageID, mmWaveLinkRFInitSubblockID, nil)
	operations[len(operations)-1].await = &mmWaveLinkPlanEvent{
		direction:       rhcpDirectionBSSToHost,
		messageID:       mmWaveLinkRFAsyncMessageID,
		subblockID:      mmWaveLinkRFInitEventSubblockID,
		dataLength:      mmWaveLinkRFInitEventDataLength,
		calibrationMask: mmWaveLinkRFInitSuccessMask,
	}

	appendOperation(rhcpDirectionHostToMSS, mmWaveLinkDeviceConfigMessageID, mmWaveLinkDeviceDataPathSubblockID, []byte{1, 1, 0, 0, 0, 0, 0, 0})
	appendOperation(rhcpDirectionHostToMSS, mmWaveLinkDeviceConfigMessageID, mmWaveLinkDeviceClockSubblockID, []byte{1, 1, 0, 0})
	appendOperation(rhcpDirectionHostToBSS, mmWaveLinkRFStaticConfigMessageID, mmWaveLinkRFHSIClockSubblockID, []byte{9, 0, 0, 0})
	appendOperation(rhcpDirectionHostToMSS, mmWaveLinkDeviceConfigMessageID, mmWaveLinkDeviceLaneEnableSubblockID, laneEnable)
	appendOperation(rhcpDirectionHostToMSS, mmWaveLinkDeviceConfigMessageID, mmWaveLinkDeviceLVDSSubblockID, []byte{0, 0, 1, 0})

	appendOperation(rhcpDirectionHostToBSS, mmWaveLinkRFDynamicConfigMessageID, mmWaveLinkRFProfileSubblockID, encodeProfile(configuration.profile))
	for _, chirp := range configuration.chirps {
		appendOperation(rhcpDirectionHostToBSS, mmWaveLinkRFDynamicConfigMessageID, mmWaveLinkRFChirpSubblockID, encodeChirp(chirp))
	}
	appendOperation(rhcpDirectionHostToBSS, mmWaveLinkRFMiscConfigMessageID, mmWaveLinkRFTestSourceEnableID, make([]byte, 4))
	appendOperation(rhcpDirectionHostToBSS, mmWaveLinkRFDynamicConfigMessageID, mmWaveLinkRFFrameSubblockID, encodeFrame(configuration.frame, configuration.profile.samples))
	appendOperation(rhcpDirectionHostToMSS, mmWaveLinkDeviceApplyMessageID, mmWaveLinkDeviceFrameApplySubblockID, encodeFrameApply(configuration.frame, configuration.profile.samples))
	return operations, nil
}

func encodeChannel(configuration mmWaveLinkConfiguration) []byte {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint16(data[0:2], configuration.rxMask)
	binary.LittleEndian.PutUint16(data[2:4], configuration.txMask)
	return data
}

func encodeADC(configuration mmWaveLinkConfiguration) []byte {
	data := make([]byte, 8)
	format := uint32(configuration.adcBits) | uint32(configuration.adcFormat)<<16
	binary.LittleEndian.PutUint32(data[0:4], format)
	return data
}

func encodeDataFormat(configuration mmWaveLinkConfiguration) []byte {
	data := make([]byte, 12)
	binary.LittleEndian.PutUint16(data[0:2], configuration.rxMask)
	binary.LittleEndian.PutUint16(data[2:4], configuration.adcBits)
	binary.LittleEndian.PutUint16(data[4:6], configuration.adcFormat)
	data[6] = configuration.iqSwap
	data[7] = configuration.interleave
	return data
}

func encodeProfile(profile mmWaveLinkProfileConfiguration) []byte {
	data := make([]byte, 44)
	binary.LittleEndian.PutUint32(data[4:8], profile.startFrequency)
	binary.LittleEndian.PutUint32(data[8:12], profile.idleTime)
	binary.LittleEndian.PutUint32(data[12:16], profile.adcStartTime)
	binary.LittleEndian.PutUint32(data[16:20], profile.rampEndTime)
	binary.LittleEndian.PutUint32(data[20:24], profile.powerBackoff)
	binary.LittleEndian.PutUint32(data[24:28], profile.phaseShifter)
	binary.LittleEndian.PutUint16(data[28:30], uint16(profile.frequencySlope))
	binary.LittleEndian.PutUint16(data[30:32], uint16(profile.txStartTime))
	binary.LittleEndian.PutUint16(data[32:34], profile.samples)
	binary.LittleEndian.PutUint16(data[34:36], profile.sampleRate)
	data[36], data[37] = profile.hpf1, profile.hpf2
	binary.LittleEndian.PutUint16(data[40:42], profile.rxGain)
	return data
}

func encodeChirp(chirp mmWaveLinkChirpConfiguration) []byte {
	data := make([]byte, 20)
	binary.LittleEndian.PutUint16(data[0:2], chirp.start)
	binary.LittleEndian.PutUint16(data[2:4], chirp.end)
	binary.LittleEndian.PutUint16(data[4:6], chirp.profileID)
	binary.LittleEndian.PutUint32(data[8:12], chirp.startVariation)
	binary.LittleEndian.PutUint16(data[12:14], chirp.slopeVariation)
	binary.LittleEndian.PutUint16(data[14:16], chirp.idleVariation)
	binary.LittleEndian.PutUint16(data[16:18], chirp.adcVariation)
	binary.LittleEndian.PutUint16(data[18:20], chirp.txMask)
	return data
}

func encodeFrame(frame mmWaveLinkFrameConfiguration, samples uint16) []byte {
	data := make([]byte, 24)
	binary.LittleEndian.PutUint16(data[2:4], frame.chirpStart)
	binary.LittleEndian.PutUint16(data[4:6], frame.chirpEnd)
	binary.LittleEndian.PutUint16(data[6:8], frame.loops)
	binary.LittleEndian.PutUint16(data[8:10], frame.frames)
	binary.LittleEndian.PutUint16(data[10:12], samples*2)
	binary.LittleEndian.PutUint32(data[12:16], frame.period)
	binary.LittleEndian.PutUint16(data[16:18], 1)
	return data
}

func encodeFrameApply(frame mmWaveLinkFrameConfiguration, samples uint16) []byte {
	data := make([]byte, 8)
	chirps := uint32(frame.chirpEnd-frame.chirpStart+1) * uint32(frame.loops)
	binary.LittleEndian.PutUint32(data[0:4], chirps)
	binary.LittleEndian.PutUint16(data[4:6], samples*2)
	return data
}
