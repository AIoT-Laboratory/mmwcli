package debugcapture

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mmwcli/internal/serialport"
)

const (
	enhancedCOMBaud             = 115200
	enhancedCOMPreOpenWait      = 400 * time.Millisecond
	enhancedCOMOpenTimeout      = time.Second
	enhancedCOMOperationTimeout = 5 * time.Second
	enhancedCOMFirmwareTimeout  = 2 * time.Minute

	xwr68xxEFUSERow10Address = uint32(0xffffe214)
	xwr68xxPartNumberShift   = 18
	xwr68xxPartNumberMask    = uint32(0xff)
	iwr68xxES2PartNumber     = uint8(0xe2)
	awr68xxPartNumber        = uint8(0x51)
)

type enhancedCOMBackend struct {
	open func(string, int, time.Duration) (enhancedCOMTransport, error)
	wait func(context.Context, time.Duration) error
}

type enhancedCOMConnection struct {
	client     *enhancedCOMClient
	probeValue uint32
	partNumber uint8
	verified   bool
}

// openEnhancedCOMConnection opens only the explicitly named port at the
// xWR68xx cold-start boot-monitor baud. It deliberately does not scan ports,
// probe an alternate baud rate, or perform Studio's fallback/reconnect flow.
func openEnhancedCOMConnection(ctx context.Context, portName string) (*enhancedCOMConnection, error) {
	return openEnhancedCOMConnectionWithBackend(ctx, portName, enhancedCOMBackend{
		open: func(name string, baud int, timeout time.Duration) (enhancedCOMTransport, error) {
			return serialport.Open(name, baud, timeout)
		},
		wait: waitContext,
	})
}

func openEnhancedCOMConnectionWithBackend(
	ctx context.Context,
	portName string,
	backend enhancedCOMBackend,
) (*enhancedCOMConnection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if backend.open == nil {
		return nil, errors.New("Enhanced COM opener is nil")
	}
	if backend.wait == nil {
		return nil, errors.New("Enhanced COM wait function is nil")
	}
	if err := backend.wait(ctx, enhancedCOMPreOpenWait); err != nil {
		return nil, err
	}

	transport, err := backend.open(portName, enhancedCOMBaud, enhancedCOMOpenTimeout)
	if err != nil {
		return nil, fmt.Errorf("open Enhanced COM port %q: %w", portName, err)
	}
	client, err := newEnhancedCOMClient(transport, enhancedCOMOperationTimeout)
	if err != nil {
		return nil, errors.Join(err, transport.Close())
	}
	client.wait = backend.wait
	probeValue, err := client.initialize(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("initialize Enhanced COM port %q: %w", portName, err), client.close())
	}
	efuseRow10, err := client.readRegister(ctx, xwr68xxEFUSERow10Address)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("read xWR68xx part identity on %q: %w", portName, err), client.close())
	}
	partNumber := uint8((efuseRow10 >> xwr68xxPartNumberShift) & xwr68xxPartNumberMask)
	if !supportedXWR6843Part(partNumber) {
		return nil, errors.Join(
			fmt.Errorf(
				"Enhanced COM target on %q has unsupported part number 0x%02X; expected IWR68xx ES2 0x%02X or AWR68xx 0x%02X",
				portName,
				partNumber,
				iwr68xxES2PartNumber,
				awr68xxPartNumber,
			),
			client.close(),
		)
	}
	return &enhancedCOMConnection{
		client:     client,
		probeValue: probeValue,
		partNumber: partNumber,
		verified:   true,
	}, nil
}

func supportedXWR6843Part(partNumber uint8) bool {
	return partNumber == iwr68xxES2PartNumber || partNumber == awr68xxPartNumber
}

func (connection *enhancedCOMConnection) close() error {
	if connection == nil || connection.client == nil {
		return nil
	}
	return connection.client.close()
}

// submitFirmware keeps the connection open after a complete host submission
// so a later SPI/mmWaveLink verification step can establish device-side
// success. Any failed submission closes the poisoned or non-ready connection
// without attempting register cleanup or reset.
func (connection *enhancedCOMConnection) submitFirmware(
	ctx context.Context,
	assets Assets,
) (firmwareSubmissionReceipt, error) {
	if connection == nil || connection.client == nil {
		return firmwareSubmissionReceipt{}, errors.New("Enhanced COM connection is nil")
	}
	if !connection.verified || !supportedXWR6843Part(connection.partNumber) {
		return firmwareSubmissionReceipt{}, errors.Join(
			errors.New("Enhanced COM connection has not passed the xWR6843 SOP2 monitor and part identity gate"),
			connection.close(),
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	submissionContext, cancel := context.WithTimeout(ctx, enhancedCOMFirmwareTimeout)
	defer cancel()
	downloader, err := newFirmwareDownloader(connection.client)
	if err != nil {
		return firmwareSubmissionReceipt{}, errors.Join(err, connection.close())
	}
	receipt, err := downloader.submit(submissionContext, assets)
	if err != nil {
		return firmwareSubmissionReceipt{}, errors.Join(err, connection.close())
	}
	return receipt, nil
}
