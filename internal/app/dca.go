package app

import (
	"fmt"
	"net"

	"mmwcli/internal/dca"
)

type dcaConfig struct {
	control  dca.Options
	receiver dca.ReceiverConfig
	fpga     dca.FPGAConfig
	delay    int
}

func dcaForSetup(setup setupConfig) (dcaConfig, error) {
	control := dca.DefaultOptions()
	receiver := dca.DefaultReceiverConfig()
	deviceIP, err := parseSetupIPv4("dca.device", setup.DCA.Device)
	if err != nil {
		return dcaConfig{}, err
	}
	hostIP, err := parseSetupIPv4("dca.host", setup.DCA.Host)
	if err != nil {
		return dcaConfig{}, err
	}
	fpga := dca.DefaultFPGAConfig()
	control.DeviceAddress = deviceIP
	receiver.DataBindAddress = hostIP
	receiver.DeviceIP = deviceIP
	return dcaConfig{
		control:  control,
		receiver: receiver,
		fpga:     fpga,
		delay:    setup.DCA.DelayUS,
	}, nil
}

func parseSetupIPv4(name, value string) (net.IP, error) {
	address := net.ParseIP(value)
	if address == nil || address.To4() == nil {
		return nil, fmt.Errorf("%s must be an IPv4 address: %s", name, value)
	}
	return address.To4(), nil
}

func summarizeCapture(stats dca.CaptureStats) string {
	return fmt.Sprintf(
		"capture complete: frames-bytes=%d packets=%d gaps=%d missing=%d",
		stats.OutputBytes,
		stats.PacketsReceived,
		stats.SequenceGaps,
		stats.MissingBytes,
	)
}
