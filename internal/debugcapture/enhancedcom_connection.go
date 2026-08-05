package debugcapture

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mmwcli/internal/serialport"
)

const (
	enhancedCOMBaud             = 921600
	enhancedCOMPreOpenWait      = 400 * time.Millisecond
	enhancedCOMOpenTimeout      = time.Second
	enhancedCOMOperationTimeout = 5 * time.Second
	enhancedCOMFirmwareTimeout  = 2 * time.Minute
)

type enhancedCOMBackend struct {
	open func(string, int, time.Duration) (enhancedCOMTransport, error)
	wait func(context.Context, time.Duration) error
}

type enhancedCOMConnection struct {
	client     *enhancedCOMClient
	probeValue uint32
}

// openEnhancedCOMConnection opens only the explicitly named port at the
// standard xWR68xx Studio debug baud. It deliberately does not scan ports or
// probe an alternate baud rate.
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
	return &enhancedCOMConnection{client: client, probeValue: probeValue}, nil
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
