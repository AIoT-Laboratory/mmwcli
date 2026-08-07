// Package radar implements the host side of the xWR68xx text CLI protocols.
//
// It deliberately contains no serial-port implementation. Callers provide an
// io.ReadWriteCloser that has already been opened and configured for the baud
// rate advertised by the selected Dialect.
package radar

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Dialect describes one device-side text CLI firmware family.
type Dialect struct {
	name              string
	defaultBaud       int
	requiresPlatform  bool
	restrictToRawOnly bool
}

var (
	// SDKDemo is the text CLI exposed by the mmWave SDK demo firmware.
	SDKDemo = Dialect{name: "demo", defaultBaud: 115200, requiresPlatform: true}

	// StudioCLI is TI's xWR68xx studio_cli device firmware dialect.
	StudioCLI = Dialect{
		name:              "studio-cli",
		defaultBaud:       921600,
		requiresPlatform:  true,
		restrictToRawOnly: true,
	}
)

func (d Dialect) Name() string { return d.name }

func (d Dialect) DefaultBaud() int { return d.defaultBaud }

func (d Dialect) RequiresPlatformVerification() bool { return d.requiresPlatform }

func (d Dialect) valid() bool {
	return d.name != "" && d.defaultBaud > 0
}

// TerminalStatus is the state found while accumulating a command response.
type TerminalStatus uint8

const (
	TerminalPending TerminalStatus = iota
	TerminalDone
	TerminalError
)

// TerminalResult identifies a strict terminal response line. ErrorCode is
// meaningful only when Status is TerminalError.
type TerminalResult struct {
	Status    TerminalStatus
	ErrorCode int
}

// FindTerminal recognizes only an exact "Done" line or an exact numeric
// "Error <code>" line. In particular, diagnostic lines beginning with
// "Error:" are not command failures.
func FindTerminal(response string) TerminalResult {
	for _, raw := range strings.Split(strings.ReplaceAll(response, "\r", ""), "\n") {
		line := strings.TrimSpace(raw)
		if line == "Done" {
			return TerminalResult{Status: TerminalDone}
		}
		if code, ok := parseErrorLine(line); ok {
			return TerminalResult{Status: TerminalError, ErrorCode: code}
		}
	}
	return TerminalResult{Status: TerminalPending}
}

func parseErrorLine(line string) (int, bool) {
	const prefix = "Error "
	if !strings.HasPrefix(line, prefix) {
		return 0, false
	}
	value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if value == "" || strings.ContainsAny(value, " \t") {
		return 0, false
	}
	code, err := strconv.Atoi(value)
	return code, err == nil
}

// CommandError reports an explicit numeric error returned by the radar CLI.
type CommandError struct {
	Command     string
	Response    string
	Code        int
	Description string
}

func (e *CommandError) Error() string {
	detail := ""
	if e.Description != "" {
		detail = ": " + e.Description
	}
	return fmt.Sprintf("radar rejected %q with Error %d%s", e.Command, e.Code, detail)
}

// StudioErrorDescription maps the errors implemented by the audited xWR68xx
// studio_cli device firmware. The boolean is false outside that range.
func StudioErrorDescription(code int) (string, bool) {
	description, ok := studioErrorDescriptions[code]
	return description, ok
}

var studioErrorDescriptions = map[int]string{
	-50: "invalid command",
	-51: "invalid command usage",
	-52: "invalid input parameter",
	-53: "sensorStart reconfiguration argument must be omitted or zero",
	-54: "sensor is already in the requested start/stop state",
	-55: "LVDS software or header streaming is unsupported",
	-56: "command timed out",
	-57: "data-path configuration failed",
	-58: "configuration command is not allowed after sensorStart",
}

func newCommandError(dialect Dialect, command, response string, code int) error {
	description := ""
	if dialect == StudioCLI {
		description, _ = StudioErrorDescription(code)
	}
	return &CommandError{
		Command:     command,
		Response:    response,
		Code:        code,
		Description: description,
	}
}

// IsAlreadyStopped reports only the studio_cli -54 response to sensorStop as
// idempotent success. The same code from sensorStart remains an error.
func IsAlreadyStopped(err error) bool {
	commandError, ok := errors.AsType[*CommandError](err)
	return ok &&
		commandError.Code == -54 && commandError.Command == "sensorStop"
}

// VerifyPlatformResponse enforces the xWR68xx version gate for dialects that
// require platform verification.
func (d Dialect) VerifyPlatformResponse(response string) error {
	if !d.valid() {
		return errors.New("invalid radar CLI dialect")
	}
	if !d.requiresPlatform {
		return nil
	}

	for _, raw := range strings.Split(strings.ReplaceAll(response, "\r", ""), "\n") {
		name, value, ok := strings.Cut(raw, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "Platform") {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(value), "xWR68xx") {
			return nil
		}
		return fmt.Errorf("version response reported unsupported platform %q; expected xWR68xx", strings.TrimSpace(value))
	}
	return errors.New("version response did not contain Platform: xWR68xx")
}

var studioRawCommands = map[string]string{
	"flushcfg":          "flushCfg",
	"dfedataoutputmode": "dfeDataOutputMode",
	"channelcfg":        "channelCfg",
	"adccfg":            "adcCfg",
	"adcbufcfg":         "adcbufCfg",
	"profilecfg":        "profileCfg",
	"chirpcfg":          "chirpCfg",
	"framecfg":          "frameCfg",
	"lowpower":          "lowPower",
	"lvdsstreamcfg":     "lvdsStreamCfg",
	"sensorstart":       "sensorStart",
}

var studioMonitorCommands = map[string]struct{}{
	"guimonitor": {}, "cqrxsatmonitor": {}, "cqsigimgmonitor": {},
	"calibmoncfg": {}, "moncalibreportcfg": {}, "gpadcsigmoncfg": {},
	"tempmoncfg": {}, "extanasigmoncfg": {}, "txpowermoncfg": {},
	"txballbreakmoncfg": {}, "rxgainphasemoncfg": {}, "synthfreqmoncfg": {},
	"pllconvoltmoncfg": {}, "dualclkcompmoncfg": {}, "rxifstagemoncfg": {},
	"pmclksigmoncfg": {}, "rxintanasigmoncfg": {}, "txintanasigmoncfg": {},
}

var studioOutOfScopeCommands = map[string]struct{}{
	"devicerestart": {}, "advframecfg": {}, "subframecfg": {},
	"contmodecfg": {}, "ldobypass": {}, "calibconfig": {}, "hsiclkcfg": {},
	"testsrcobj": {}, "testsrccfg": {}, "loopbackcfg": {}, "ifloopcfg": {},
	"psloopcfg": {}, "paloopcfg": {}, "misccfg": {},
}

// ValidateConfiguration enforces the audited raw-only command surface of the
// studio_cli dialect. When full is true, flushCfg must occur exactly once
// and be the first command. A flushCfg supplied in reuse mode must still be
// first; reuse does not imply that flushCfg clears profile/chirp counters.
func (d Dialect) ValidateConfiguration(commands []string, full bool) error {
	if !d.valid() {
		return errors.New("invalid radar CLI dialect")
	}
	if !d.restrictToRawOnly {
		return nil
	}

	flushes := 0
	for index, command := range commands {
		fields := strings.Fields(command)
		if len(fields) == 0 {
			return fmt.Errorf("configuration command %d is empty", index+1)
		}
		name := fields[0]
		key := strings.ToLower(name)
		canonical, allowed := studioRawCommands[key]
		if !allowed {
			if _, monitor := studioMonitorCommands[key]; monitor {
				return fmt.Errorf("raw-only studio_cli does not consume UART monitor reports; rejected command: %s", command)
			}
			if _, outOfScope := studioOutOfScopeCommands[key]; outOfScope {
				return fmt.Errorf("raw-only studio_cli does not support reset, advanced/continuous, or test/loopback command: %s", command)
			}
			return fmt.Errorf("command is not in the audited xWR68xx studio_cli raw-only allowlist: %s", command)
		}
		if name != canonical {
			return fmt.Errorf("xWR68xx CLI command names are case-sensitive; use %s: %s", canonical, command)
		}

		if name == "flushCfg" {
			if len(fields) != 1 {
				return fmt.Errorf("flushCfg takes no arguments: %s", command)
			}
			flushes++
			if index != 0 {
				return errors.New("flushCfg must be the first command in a full studio_cli configuration")
			}
		}
	}
	if flushes > 1 {
		return errors.New("studio_cli configuration may contain at most one flushCfg")
	}
	if full && flushes != 1 {
		return errors.New("full studio_cli configuration must begin with exactly one flushCfg; use reuse mode for a second capture")
	}
	return nil
}
