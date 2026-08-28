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

func dcaForRig(rig rigConfig) (dcaConfig, error) {
	control := dca.DefaultOptions()
	receiver := dca.DefaultReceiverConfig()
	deviceIP, err := parseRigIPv4("dca.device", rig.DCA.Device)
	if err != nil {
		return dcaConfig{}, err
	}
	hostIP, err := parseRigIPv4("dca.host", rig.DCA.Host)
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
		delay:    rig.DCA.DelayUS,
	}, nil
}

func parseRigIPv4(name, value string) (net.IP, error) {
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
