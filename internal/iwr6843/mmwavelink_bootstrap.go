package iwr6843

import (
	"context"
	"encoding/binary"
	"fmt"
)

const (
	mmWaveLinkMSSPowerupDoneSubblockID = 0
	mmWaveLinkRFPowerupDoneSubblockID  = 1
	mmWaveLinkDevicePowerupMessageID   = 0x200
	mmWaveLinkDeviceStatusGetMessageID = 0x207
	mmWaveLinkRFStatusGetMessageID     = 0x011
	mmWaveLinkVersionSubblockID        = 0
	mmWaveLinkVersionLength            = 16
	mmWaveLinkRFPowerupStatusDone      = 2
)

type mmWaveLinkFirmwareVersion struct {
	HardwareVariant uint8
	HardwareMajor   uint8
	HardwareMinor   uint8
	FirmwareMajor   uint8
	FirmwareMinor   uint8
	FirmwareBuild   uint8
	FirmwareDebug   uint8
	FirmwareYear    uint8
	FirmwareMonth   uint8
	FirmwareDay     uint8
	PatchMajor      uint8
	PatchMinor      uint8
	PatchYear       uint8
	PatchMonth      uint8
	PatchDay        uint8
	PatchBuildDebug uint8
}

type mmWaveLinkFirmwareRelease struct {
	Major uint8
	Minor uint8
	Build uint8
	Debug uint8
}

func (version mmWaveLinkFirmwareVersion) release() mmWaveLinkFirmwareRelease {
	return mmWaveLinkFirmwareRelease{
		Major: version.FirmwareMajor,
		Minor: version.FirmwareMinor,
		Build: version.FirmwareBuild,
		Debug: version.FirmwareDebug,
	}
}

func (release mmWaveLinkFirmwareRelease) String() string {
	return fmt.Sprintf("%d.%d.%d.%d", release.Major, release.Minor, release.Build, release.Debug)
}

type mmWaveLinkDeviceDiagnostics struct {
	MSS             mmWaveLinkFirmwareVersion
	RF              mmWaveLinkFirmwareVersion
	RFPowerupStatus uint32
}

func bootstrapMMWaveLink(ctx context.Context, client *mmWaveLinkClient) (mmWaveLinkDeviceDiagnostics, error) {
	var diagnostics mmWaveLinkDeviceDiagnostics
	if client == nil {
		return diagnostics, fmt.Errorf("mmWaveLink client is required")
	}

	mssPowerup, err := client.waitEvent(
		ctx,
		mmWaveLinkDeviceAsyncMessageID,
		mmWaveLinkMSSPowerupDoneSubblockID,
	)
	if err != nil {
		return diagnostics, fmt.Errorf("wait for MSS power-up: %w", err)
	}
	if _, err := bootstrapEventData(mssPowerup, mmWaveLinkMSSPowerupDoneSubblockID); err != nil {
		return diagnostics, fmt.Errorf("validate MSS power-up: %w", err)
	}

	diagnostics.MSS, err = queryMMWaveLinkVersion(
		ctx,
		client,
		"MSS",
		rhcpDirectionHostToMSS,
		mmWaveLinkDeviceStatusGetMessageID,
		iwr6843Runtime.mss,
	)
	if err != nil {
		return diagnostics, err
	}

	rfStart, err := client.execute(ctx, mmWaveLinkCommand{
		direction: rhcpDirectionHostToMSS,
		messageID: mmWaveLinkDevicePowerupMessageID,
		subblocks: []mmWaveLinkSubblock{{id: 0}},
	})
	if err != nil {
		return diagnostics, fmt.Errorf("start RF subsystem: %w", err)
	}
	if len(rfStart.subblocks) != 0 {
		return diagnostics, fmt.Errorf("RF start response has %d sub-blocks; expected a header-only response", len(rfStart.subblocks))
	}

	rfPowerup, err := client.waitEvent(
		ctx,
		mmWaveLinkDeviceAsyncMessageID,
		mmWaveLinkRFPowerupDoneSubblockID,
	)
	if err != nil {
		return diagnostics, fmt.Errorf("wait for RF power-up: %w", err)
	}
	rfPowerupData, err := bootstrapEventData(rfPowerup, mmWaveLinkRFPowerupDoneSubblockID)
	if err != nil {
		return diagnostics, fmt.Errorf("validate RF power-up: %w", err)
	}
	diagnostics.RFPowerupStatus = binary.LittleEndian.Uint32(rfPowerupData[:4])
	if diagnostics.RFPowerupStatus != mmWaveLinkRFPowerupStatusDone {
		return diagnostics, fmt.Errorf(
			"RF power-up status is %#08x; expected %#08x",
			diagnostics.RFPowerupStatus,
			uint32(mmWaveLinkRFPowerupStatusDone),
		)
	}

	diagnostics.RF, err = queryMMWaveLinkVersion(
		ctx,
		client,
		"RF",
		rhcpDirectionHostToBSS,
		mmWaveLinkRFStatusGetMessageID,
		iwr6843Runtime.rf,
	)
	if err != nil {
		return diagnostics, err
	}
	return diagnostics, nil
}

func bootstrapEventData(event mmWaveLinkMessage, subblockID uint16) ([]byte, error) {
	if event.direction != rhcpDirectionMSSToHost ||
		event.messageClass != rhcpMessageClassAsync ||
		event.messageID != mmWaveLinkDeviceAsyncMessageID ||
		len(event.subblocks) != 1 ||
		event.subblocks[0].id != subblockID ||
		len(event.subblocks[0].data) != 16 {
		return nil, fmt.Errorf(
			"power-up event sub-block %#02x must be the only sub-block and contain exactly 16 data bytes",
			subblockID,
		)
	}
	return event.subblocks[0].data, nil
}

func queryMMWaveLinkVersion(
	ctx context.Context,
	client *mmWaveLinkClient,
	component string,
	direction rhcpDirection,
	messageID uint16,
	expected mmWaveLinkFirmwareRelease,
) (mmWaveLinkFirmwareVersion, error) {
	var version mmWaveLinkFirmwareVersion
	response, err := client.execute(ctx, mmWaveLinkCommand{
		direction: direction,
		messageID: messageID,
		subblocks: []mmWaveLinkSubblock{{id: mmWaveLinkVersionSubblockID}},
	})
	if err != nil {
		return version, fmt.Errorf("query %s version: %w", component, err)
	}
	if response.messageClass != rhcpMessageClassResponse ||
		response.messageID != messageID ||
		len(response.subblocks) != 1 ||
		response.subblocks[0].id != mmWaveLinkVersionSubblockID ||
		len(response.subblocks[0].data) != mmWaveLinkVersionLength {
		return version, fmt.Errorf("%s version response must contain exactly one 16-byte sub-block 0", component)
	}
	version, err = decodeMMWaveLinkFirmwareVersion(response.subblocks[0].data)
	if err != nil {
		return version, fmt.Errorf("decode %s version: %w", component, err)
	}
	actual := version.release()
	if actual == expected {
		return version, nil
	}
	return version, fmt.Errorf(
		"unsupported %s firmware %s; expected %s",
		component,
		actual,
		expected,
	)
}

func decodeMMWaveLinkFirmwareVersion(data []byte) (mmWaveLinkFirmwareVersion, error) {
	if len(data) != mmWaveLinkVersionLength {
		return mmWaveLinkFirmwareVersion{}, fmt.Errorf(
			"firmware version is %d bytes; expected %d",
			len(data),
			mmWaveLinkVersionLength,
		)
	}
	return mmWaveLinkFirmwareVersion{
		HardwareVariant: data[0],
		HardwareMajor:   data[1],
		HardwareMinor:   data[2],
		FirmwareMajor:   data[3],
		FirmwareMinor:   data[4],
		FirmwareBuild:   data[5],
		FirmwareDebug:   data[6],
		FirmwareYear:    data[7],
		FirmwareMonth:   data[8],
		FirmwareDay:     data[9],
		PatchMajor:      data[10],
		PatchMinor:      data[11],
		PatchYear:       data[12],
		PatchMonth:      data[13],
		PatchDay:        data[14],
		PatchBuildDebug: data[15],
	}, nil
}
