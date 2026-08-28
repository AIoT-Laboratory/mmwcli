package radar

import (
	"errors"
	"fmt"
	"strings"
)

var rawCommands = map[string]string{
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

var monitorCommands = map[string]struct{}{
	"guimonitor": {}, "cqrxsatmonitor": {}, "cqsigimgmonitor": {},
	"calibmoncfg": {}, "moncalibreportcfg": {}, "gpadcsigmoncfg": {},
	"tempmoncfg": {}, "extanasigmoncfg": {}, "txpowermoncfg": {},
	"txballbreakmoncfg": {}, "rxgainphasemoncfg": {}, "synthfreqmoncfg": {},
	"pllconvoltmoncfg": {}, "dualclkcompmoncfg": {}, "rxifstagemoncfg": {},
	"pmclksigmoncfg": {}, "rxintanasigmoncfg": {}, "txintanasigmoncfg": {},
}

var unsupportedCommands = map[string]struct{}{
	"devicerestart": {}, "advframecfg": {}, "subframecfg": {},
	"contmodecfg": {}, "ldobypass": {}, "calibconfig": {}, "hsiclkcfg": {},
	"testsrcobj": {}, "testsrccfg": {}, "loopbackcfg": {}, "ifloopcfg": {},
	"psloopcfg": {}, "paloopcfg": {}, "misccfg": {},
}

func validateCommands(commands []string) error {
	flushes := 0
	for index, command := range commands {
		fields := strings.Fields(command)
		if len(fields) == 0 {
			return fmt.Errorf("configuration command %d is empty", index+1)
		}
		name := fields[0]
		key := strings.ToLower(name)
		canonical, allowed := rawCommands[key]
		if !allowed {
			if _, monitor := monitorCommands[key]; monitor {
				return fmt.Errorf("raw capture does not consume monitor reports: %s", command)
			}
			if _, unsupported := unsupportedCommands[key]; unsupported {
				return fmt.Errorf("raw capture rejects advanced, continuous, reset, and test commands: %s", command)
			}
			return fmt.Errorf("command is not supported by IWR6843 raw capture: %s", command)
		}
		if name != canonical {
			return fmt.Errorf("IWR6843 command names are case-sensitive; use %s: %s", canonical, command)
		}
		if name != "flushCfg" {
			continue
		}
		if len(fields) != 1 {
			return fmt.Errorf("flushCfg takes no arguments: %s", command)
		}
		flushes++
		if index != 0 {
			return errors.New("flushCfg must be the first command")
		}
	}
	if flushes != 1 {
		return errors.New("capture configuration must begin with exactly one flushCfg")
	}
	return nil
}
