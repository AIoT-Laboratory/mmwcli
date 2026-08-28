package radar

import (
	"fmt"
	"math/bits"
)

const (
	iwr6843Platform                 = "xWR68xx"
	iwr6843ReceiverMask             = uint64(0x0f)
	iwr6843TransmitterMask          = uint64(0x07)
	iwr6843MinimumStartFrequencyGHz = float64(57)
	iwr6843MaximumStartFrequencyGHz = float64(64)
	iwr6843ADCBufBytes              = 32 * 1024
)

func iwr6843ReceiverRange() string {
	return fmt.Sprintf("RX0..RX%d", bits.Len64(iwr6843ReceiverMask)-1)
}

func iwr6843TransmitterRange() string {
	return fmt.Sprintf("TX0..TX%d", bits.Len64(iwr6843TransmitterMask)-1)
}
