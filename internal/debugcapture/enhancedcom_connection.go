package debugcapture

import (
	"context"
	"errors"
	"fmt"
	"time"

	"mmwcli/internal/serialport"
)

const (
	enhancedCOMBaud                     = 921600
	enhancedCOMColdBootBaud             = 115200
	enhancedCOMPreOpenWait              = 400 * time.Millisecond
	enhancedCOMNegotiationOpenWait      = 500 * time.Millisecond
	enhancedCOMNegotiationProbeWait     = 500 * time.Millisecond
	enhancedCOMBaudRegisterWait         = 300 * time.Millisecond
	enhancedCOMNegotiationReconnectWait = 900 * time.Millisecond
	enhancedCOMOpenTimeout              = time.Second
	enhancedCOMOperationTimeout         = 5 * time.Second
	enhancedCOMFirmwareTimeout          = 2 * time.Minute

	xwr68xxBaudClockAddress    = uint32(0xffffe144)
	xwr68xxBaudClockMask       = uint32(0x00007800)
	xwr68xxBaudRegisterAddress = uint32(0xffffe264)
	xwr68xxBaud921600Value     = uint32(0x0d902c2b)
	xwr68xxEFUSERow10Address   = uint32(0xffffe214)
	xwr68xxPartNumberShift     = 18
	xwr68xxPartNumberMask      = uint32(0xff)
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
	family     debugFamilyID
}

// openEnhancedCOMConnection opens only the explicitly named port. It performs
// Studio's fixed 921600/115200 negotiation, but never scans ports or guesses
// any other rate.
func openEnhancedCOMConnection(ctx context.Context, portName string) (*enhancedCOMConnection, error) {
	return openEnhancedCOMConnectionForFamily(ctx, portName, debugFamilyIWR6843ES2)
}

func openEnhancedCOMConnectionForFamily(
	ctx context.Context,
	portName string,
	familyID debugFamilyID,
) (*enhancedCOMConnection, error) {
	return openEnhancedCOMConnectionForFamilyWithBackend(ctx, portName, familyID, enhancedCOMBackend{
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
	return openEnhancedCOMConnectionForFamilyWithBackend(
		ctx,
		portName,
		debugFamilyIWR6843ES2,
		backend,
	)
}

func openEnhancedCOMConnectionForFamilyWithBackend(
	ctx context.Context,
	portName string,
	familyID debugFamilyID,
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
	family, err := debugFamilyContractForID(familyID)
	if err != nil {
		return nil, err
	}
	if family.bootPolicy != debugBootXWR68xxRFEval {
		return nil, fmt.Errorf("unsupported debug-cli boot policy %d", family.bootPolicy)
	}

	client, probeValue, safeToNegotiate, err := openInitializedEnhancedCOMClient(ctx, portName, enhancedCOMBaud, backend)
	if err != nil {
		if !safeToNegotiate {
			return nil, err
		}
		requestedBaudErr := err
		client, probeValue, err = negotiateEnhancedCOMBaud(ctx, portName, backend, family)
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("probe Enhanced COM port %q at %d baud: %w", portName, enhancedCOMBaud, requestedBaudErr),
				err,
			)
		}
	}

	partNumber, err := gateDebugFamilyPart(ctx, client, family)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("gate xWR6843 part identity on %q: %w", portName, err), client.close())
	}
	return &enhancedCOMConnection{
		client:     client,
		probeValue: probeValue,
		partNumber: partNumber,
		verified:   true,
		family:     family.id,
	}, nil
}

func openInitializedEnhancedCOMClient(
	ctx context.Context,
	portName string,
	baud int,
	backend enhancedCOMBackend,
) (*enhancedCOMClient, uint32, bool, error) {
	if err := backend.wait(ctx, enhancedCOMPreOpenWait); err != nil {
		return nil, 0, false, err
	}
	client, err := openEnhancedCOMClient(portName, baud, backend)
	if err != nil {
		return nil, 0, false, err
	}
	probeValue, err := client.initialize(ctx)
	if err != nil {
		closeErr := client.close()
		probeRejected := errors.Is(err, errEnhancedCOMReadTimeout) || errors.Is(err, errEnhancedCOMInvalidResponse)
		return nil, 0, probeRejected && closeErr == nil, errors.Join(
			fmt.Errorf("initialize Enhanced COM port %q at %d baud: %w", portName, baud, err),
			closeErr,
		)
	}
	return client, probeValue, false, nil
}

func openEnhancedCOMClient(portName string, baud int, backend enhancedCOMBackend) (*enhancedCOMClient, error) {
	transport, err := backend.open(portName, baud, enhancedCOMOpenTimeout)
	if err != nil {
		return nil, fmt.Errorf("open Enhanced COM port %q at %d baud: %w", portName, baud, err)
	}
	client, err := newEnhancedCOMClient(transport, enhancedCOMOperationTimeout)
	if err != nil {
		return nil, errors.Join(err, transport.Close())
	}
	client.wait = backend.wait
	return client, nil
}

func negotiateEnhancedCOMBaud(
	ctx context.Context,
	portName string,
	backend enhancedCOMBackend,
	family debugFamilyContract,
) (*enhancedCOMClient, uint32, error) {
	if err := backend.wait(ctx, enhancedCOMNegotiationOpenWait); err != nil {
		return nil, 0, err
	}
	coldBootClient, err := openEnhancedCOMClient(portName, enhancedCOMColdBootBaud, backend)
	if err != nil {
		return nil, 0, err
	}
	fail := func(err error) (*enhancedCOMClient, uint32, error) {
		return nil, 0, errors.Join(err, coldBootClient.close())
	}

	if err := backend.wait(ctx, enhancedCOMNegotiationProbeWait); err != nil {
		return fail(err)
	}
	if _, err := coldBootClient.probe(ctx); err != nil {
		return fail(fmt.Errorf("probe Enhanced COM port %q at cold-boot baud %d: %w", portName, enhancedCOMColdBootBaud, err))
	}
	if _, err := gateDebugFamilyPart(ctx, coldBootClient, family); err != nil {
		return fail(fmt.Errorf("gate xWR6843 part identity at cold-boot baud %d: %w", enhancedCOMColdBootBaud, err))
	}
	if err := backend.wait(ctx, enhancedCOMBaudRegisterWait); err != nil {
		return fail(err)
	}
	clock, err := coldBootClient.readRegister(ctx, xwr68xxBaudClockAddress)
	if err != nil {
		return fail(fmt.Errorf("read xWR68xx baud clock configuration: %w", err))
	}
	if err := coldBootClient.writeRegister(ctx, xwr68xxBaudClockAddress, clock|xwr68xxBaudClockMask); err != nil {
		return fail(fmt.Errorf("configure xWR68xx baud clock: %w", err))
	}
	if err := backend.wait(ctx, enhancedCOMBaudRegisterWait); err != nil {
		return fail(err)
	}
	if err := coldBootClient.writeRegister(ctx, xwr68xxBaudRegisterAddress, xwr68xxBaud921600Value); err != nil {
		return fail(fmt.Errorf("switch xWR68xx debug monitor to %d baud: %w", enhancedCOMBaud, err))
	}
	if err := backend.wait(ctx, enhancedCOMBaudRegisterWait); err != nil {
		return fail(err)
	}
	if err := coldBootClient.close(); err != nil {
		return nil, 0, fmt.Errorf("close cold-boot Enhanced COM port %q: %w", portName, err)
	}
	if err := backend.wait(ctx, enhancedCOMNegotiationReconnectWait); err != nil {
		return nil, 0, err
	}
	client, probeValue, _, err := openInitializedEnhancedCOMClient(ctx, portName, enhancedCOMBaud, backend)
	if err != nil {
		return nil, 0, fmt.Errorf("verify Enhanced COM baud transition on %q: %w", portName, err)
	}
	return client, probeValue, nil
}

func gateDebugFamilyPart(
	ctx context.Context,
	client *enhancedCOMClient,
	family debugFamilyContract,
) (uint8, error) {
	efuseRow10, err := client.readRegister(ctx, xwr68xxEFUSERow10Address)
	if err != nil {
		return 0, fmt.Errorf("read xWR68xx part identity: %w", err)
	}
	partNumber := uint8((efuseRow10 >> xwr68xxPartNumberShift) & xwr68xxPartNumberMask)
	if partNumber != family.partNumber {
		return 0, fmt.Errorf(
			"unsupported part number 0x%02X; only validated %s part number 0x%02X is supported",
			partNumber,
			family.identity,
			family.partNumber,
		)
	}
	return partNumber, nil
}

func supportedXWR6843Part(partNumber uint8) bool {
	family, err := debugFamilyContractForID(debugFamilyIWR6843ES2)
	return err == nil && partNumber == family.partNumber
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
	family, err := debugFamilyContractForID(connection.family)
	if err != nil {
		return firmwareSubmissionReceipt{}, errors.Join(err, connection.close())
	}
	if family.bootPolicy != debugBootXWR68xxRFEval {
		return firmwareSubmissionReceipt{}, errors.Join(
			fmt.Errorf("unsupported debug-cli boot policy %d", family.bootPolicy),
			connection.close(),
		)
	}
	if !connection.verified || connection.partNumber != family.partNumber {
		return firmwareSubmissionReceipt{}, errors.Join(
			errors.New("Enhanced COM connection has not passed the xWR6843 SOP2 monitor and part identity gate"),
			connection.close(),
		)
	}
	if assets.family != connection.family {
		return firmwareSubmissionReceipt{}, errors.Join(
			fmt.Errorf("debug-cli firmware family %d does not match Enhanced COM family %d", assets.family, connection.family),
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
