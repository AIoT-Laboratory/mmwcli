package iwr6843

import (
	"bytes"
	"encoding/binary"
	"os"
	"strings"
	"testing"

	"mmwcli/internal/radar"
)

func TestBuildPlanGoldenIWR6843Configuration(t *testing.T) {
	source := goldenIWR6843Plan(t)
	plan, err := buildPlan(source)
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	operations := plan.operationsCopy()
	if len(operations) != 17 {
		t.Fatalf("operation count = %d, want 17", len(operations))
	}

	type operationIdentity struct {
		direction  rhcpDirection
		messageID  uint16
		subblockID uint16
	}
	wantOrder := []operationIdentity{
		{rhcpDirectionHostToBSS, 0x004, 0},
		{rhcpDirectionHostToBSS, 0x004, 2},
		{rhcpDirectionHostToMSS, 0x202, 1},
		{rhcpDirectionHostToBSS, 0x004, 3},
		{rhcpDirectionHostToBSS, 0x006, 0},
		{rhcpDirectionHostToMSS, 0x202, 2},
		{rhcpDirectionHostToMSS, 0x202, 4},
		{rhcpDirectionHostToBSS, 0x004, 5},
		{rhcpDirectionHostToMSS, 0x202, 3},
		{rhcpDirectionHostToMSS, 0x202, 5},
		{rhcpDirectionHostToBSS, 0x008, 0},
		{rhcpDirectionHostToBSS, 0x008, 1},
		{rhcpDirectionHostToBSS, 0x008, 1},
		{rhcpDirectionHostToBSS, 0x008, 1},
		{rhcpDirectionHostToBSS, 0x016, 3},
		{rhcpDirectionHostToBSS, 0x008, 2},
		{rhcpDirectionHostToMSS, 0x206, 0},
	}
	for index, want := range wantOrder {
		operation := operations[index]
		if operation.command.direction != want.direction ||
			operation.command.messageID != want.messageID ||
			len(operation.command.subblocks) != 1 ||
			operation.command.subblocks[0].id != want.subblockID {
			t.Fatalf("operation %d = %+v, want %+v", index, operation.command, want)
		}
		if operation.await != nil && index != 4 {
			t.Fatalf("operation %d unexpectedly awaits an event", index)
		}
	}

	assertOperationData(t, operations[0], []byte{0x0f, 0, 0x07, 0, 0, 0, 0, 0})
	assertOperationData(t, operations[1], []byte{0x02, 0, 0x01, 0, 0, 0, 0, 0})
	assertOperationData(t, operations[2], []byte{0x0f, 0, 0x02, 0, 0x01, 0, 0x01, 0x01, 0, 0, 0, 0})
	assertOperationData(t, operations[3], make([]byte, 4))
	assertOperationData(t, operations[4], nil)
	assertOperationData(t, operations[5], []byte{1, 1, 0, 0, 0, 0, 0, 0})
	assertOperationData(t, operations[6], []byte{1, 1, 0, 0})
	assertOperationData(t, operations[7], []byte{9, 0, 0, 0})
	assertOperationData(t, operations[8], []byte{3, 0, 0, 0})
	assertOperationData(t, operations[9], []byte{0, 0, 1, 0})

	wantProfile := []byte{
		0x00, 0x00, 0x00, 0x00,
		0x38, 0x8e, 0xe3, 0x58,
		0xbc, 0x02, 0x00, 0x00,
		0x58, 0x02, 0x00, 0x00,
		0x64, 0x19, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x79, 0x06,
		0x00, 0x00,
		0x00, 0x01,
		0x30, 0x11,
		0x00, 0x00,
		0x00, 0x00,
		0x1e, 0x00,
		0x00, 0x00,
	}
	assertOperationData(t, operations[10], wantProfile)
	if got := binary.LittleEndian.Uint32(operations[10].command.subblocks[0].data[4:8]); got != 0x58e38e38 {
		t.Fatalf("converted start frequency = %#x, want %#x", got, uint32(0x58e38e38))
	}
	if got := int16(binary.LittleEndian.Uint16(operations[10].command.subblocks[0].data[28:30])); got != 1657 {
		t.Fatalf("converted slope = %d, want 1657", got)
	}

	assertOperationData(t, operations[11], []byte{
		0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0,
	})
	assertOperationData(t, operations[12], []byte{
		1, 0, 1, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0,
	})
	assertOperationData(t, operations[13], []byte{
		2, 0, 2, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4, 0,
	})
	assertOperationData(t, operations[14], make([]byte, 4))
	assertOperationData(t, operations[15], []byte{
		0, 0,
		0, 0,
		2, 0,
		0x80, 0,
		0x58, 2,
		0, 2,
		0, 0x2d, 0x31, 1,
		1, 0,
		0, 0,
		0, 0, 0, 0,
	})
	assertOperationData(t, operations[16], []byte{0x80, 1, 0, 0, 0, 2, 0, 0})

	wait := operations[4].await
	if wait == nil || wait.direction != rhcpDirectionBSSToHost ||
		wait.messageID != mmWaveLinkRFAsyncMessageID ||
		wait.subblockID != 4 || wait.dataLength != 20 ||
		wait.calibrationMask != 0x1ffe {
		t.Fatalf("RF init await marker = %+v", wait)
	}
}

func TestBuildPlanRejectsInvalidWireConfiguration(t *testing.T) {
	source := goldenIWR6843Plan(t)
	profile := "profileCfg 0 60 7 3 24 0 0 166 1 256 12500 0 0 158"
	chirp := "chirpCfg 0 0 0 0 0 0 0 1"
	tests := []struct {
		name   string
		mutate func(radar.Plan) radar.Plan
		match  string
	}{
		{name: "profile nonfinite", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 60 ", " NaN ", 1)), match: "finite"},
		{name: "profile start range", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 60 ", " 56 ", 1)), match: "outside"},
		{name: "profile start odd", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 60 ", " 60.00000001 ", 1)), match: "must be even"},
		{name: "profile start overflow", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 60 ", " 1e300 ", 1)), match: "overflows"},
		{name: "profile idle range", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 7 3 ", " 5242.9 3 ", 1)), match: "overflows"},
		{name: "profile power range", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 0 0 166 ", " 27 0 166 ", 1)), match: "above 26"},
		{name: "profile phase reserved", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 0 0 166 ", " 0 1 166 ", 1)), match: "reserved"},
		{name: "profile TX time range", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 166 1 256 ", " 166 41 256 ", 1)), match: "overflows"},
		{name: "profile rate range", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 12500 ", " 1999 ", 1)), match: "2000..25000"},
		{name: "profile HPF range", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 0 0 158", " 4 0 158", 1)), match: "exceeds"},
		{name: "profile gain range", mutate: replaceDebugCommand("profileCfg", strings.Replace(profile, " 0 0 158", " 0 0 31", 1)), match: "invalid"},
		{name: "chirp nonfinite", mutate: replaceDebugCommand("chirpCfg", strings.Replace(chirp, " 0 0 0 1", " NaN 0 0 1", 1)), match: "finite"},
		{name: "chirp start odd", mutate: replaceDebugCommand("chirpCfg", strings.Replace(chirp, " 0 0 0 1", " 41 0 0 1", 1)), match: "must be even"},
		{name: "chirp slope odd", mutate: replaceDebugCommand("chirpCfg", "chirpCfg 0 0 0 0 37 0 0 1"), match: "must be even"},
		{name: "chirp time range", mutate: replaceDebugCommand("chirpCfg", strings.Replace(chirp, " 0 0 0 1", " 0 0 41 1", 1)), match: "overflows"},
		{name: "chirp without transmitter", mutate: replaceDebugCommand("chirpCfg", "chirpCfg 0 0 0 0 0 0 0 0"), match: "at least one transmitter"},
		{name: "low power value", mutate: replaceDebugCommand("lowPower", "lowPower 0 1"), match: "lowPower 0 0"},
		{name: "duplicate low power", mutate: appendDebugCommand("lowPower 0 0"), match: "exactly one lowPower"},
		{name: "duplicate profile", mutate: appendDebugCommand(profile), match: "profileCfg"},
		{name: "six chirps", mutate: insertDebugCommandsBefore("frameCfg",
			"chirpCfg 3 3 0 0 0 0 0 1",
			"chirpCfg 4 4 0 0 0 0 0 1",
			"chirpCfg 5 5 0 0 0 0 0 1",
		), match: "five chirpCfg"},
		{name: "unknown command", mutate: appendDebugCommand("guiMonitor -1 0 0 0 0 0 0"), match: "monitor reports"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildPlan(test.mutate(clonePlan(source)))
			if err == nil {
				t.Fatal("buildPlan accepted invalid configuration")
			}
			if !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %q, want substring %q", err, test.match)
			}
		})
	}
}

func TestBuildPlanEncodesXWR68xxReferenceSlope(t *testing.T) {
	source := goldenIWR6843Plan(t)
	source = replaceDebugCommand(
		"profileCfg",
		"profileCfg 0 60 7 6 65 0 0 60.012 0 256 4400 0 0 30",
	)(source)
	plan, err := buildPlan(source)
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	operations := plan.operationsCopy()
	got := int16(binary.LittleEndian.Uint16(operations[10].command.subblocks[0].data[28:30]))
	if got != 1657 {
		t.Fatalf("converted reference slope = %d, want 1657", got)
	}
}

func TestBuildPlanRequiresExactPlan(t *testing.T) {
	source := goldenIWR6843Plan(t)

	forged := clonePlan(source)
	forged.ExpectedBytes++
	if _, err := buildPlan(forged); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("metadata mismatch error = %v", err)
	}
}

func TestBuildPlanEncodesContinuousFrameCount(t *testing.T) {
	source := goldenIWR6843Plan(t)
	commands := append([]string(nil), source.ConfigurationCommands...)
	for index, command := range commands {
		fields := strings.Fields(command)
		if len(fields) != 0 && fields[0] == "frameCfg" {
			fields[4] = "0"
			commands[index] = strings.Join(fields, " ")
		}
	}
	if source.DeclaredStartCommand != "" {
		commands = append(commands, source.DeclaredStartCommand)
	}
	continuous, err := radar.CommandPlan(commands)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(continuous)
	if err != nil {
		t.Fatal(err)
	}
	frame := plan.operationsCopy()[15].command.subblocks[0].data
	if continuous.NumberOfFrames != 0 || continuous.ExpectedBytes != 0 ||
		binary.LittleEndian.Uint16(frame[8:10]) != 0 {
		t.Fatalf("continuous frame plan=%+v wire=% X", continuous, frame)
	}
}

func TestPlanCopiesOperationsAndSource(t *testing.T) {
	source := goldenIWR6843Plan(t)
	original := clonePlan(source)
	plan, err := buildPlan(source)
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	if !plan.matches(original) {
		t.Fatal("plan does not match its original capture plan")
	}

	source.ConfigurationCommands[0] = "corrupted"
	if !plan.matches(original) {
		t.Fatal("caller mutation changed the stored source snapshot")
	}
	if plan.matches(source) {
		t.Fatal("plan accepted a capture plan different from its preflight source")
	}

	first := plan.operationsCopy()
	first[0].command.subblocks[0].data[0] = 0
	first[0].command.subblocks = append(first[0].command.subblocks, mmWaveLinkSubblock{id: 31})
	first[4].await.dataLength = 1
	second := plan.operationsCopy()
	if second[0].command.subblocks[0].data[0] != 0x0f || len(second[0].command.subblocks) != 1 {
		t.Fatal("caller mutation changed stored operation data")
	}
	if second[4].await == nil || second[4].await.dataLength != 20 {
		t.Fatal("caller mutation changed stored event marker")
	}
}

func TestRFInitEventValidation(t *testing.T) {
	plan, err := buildPlan(goldenIWR6843Plan(t))
	if err != nil {
		t.Fatalf("buildPlan: %v", err)
	}
	expected := plan.operationsCopy()[4].await
	data := make([]byte, 20)
	binary.LittleEndian.PutUint32(data, 0x1fff)
	message := mmWaveLinkMessage{
		direction:    rhcpDirectionBSSToHost,
		messageClass: rhcpMessageClassAsync,
		messageID:    mmWaveLinkRFAsyncMessageID,
		subblocks:    []mmWaveLinkSubblock{{id: 4, data: data}},
	}
	if err := expected.validate(message); err != nil {
		t.Fatalf("validate successful RF init event: %v", err)
	}
	binary.LittleEndian.PutUint32(data, 0x1ffc)
	if err := expected.validate(message); err == nil || !strings.Contains(err.Error(), "calibration status") {
		t.Fatalf("failed calibration error = %v", err)
	}
	message.direction = rhcpDirectionMSSToHost
	if err := expected.validate(message); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("wrong event identity error = %v", err)
	}
}

func goldenIWR6843Plan(t *testing.T) radar.Plan {
	t.Helper()
	file, err := os.Open("../../hardware/iwr6843.cfg")
	if err != nil {
		t.Fatalf("open golden CFG: %v", err)
	}
	defer file.Close()
	commands, err := radar.ParseConfig(file)
	if err != nil {
		t.Fatalf("parse golden CFG: %v", err)
	}
	plan, err := radar.CommandPlan(commands)
	if err != nil {
		t.Fatalf("build golden capture plan: %v", err)
	}
	return plan
}

func assertOperationData(t *testing.T, operation mmWaveLinkPlanOperation, want []byte) {
	t.Helper()
	if len(operation.command.subblocks) != 1 {
		t.Fatalf("sub-block count = %d, want 1", len(operation.command.subblocks))
	}
	got := operation.command.subblocks[0].data
	if !bytes.Equal(got, want) {
		t.Fatalf("operation payload = % X\nwant              = % X", got, want)
	}
}

func replaceDebugCommand(name, replacement string) func(radar.Plan) radar.Plan {
	return func(plan radar.Plan) radar.Plan {
		for index, command := range plan.ConfigurationCommands {
			fields := strings.Fields(command)
			if len(fields) != 0 && fields[0] == name {
				plan.ConfigurationCommands[index] = replacement
				return plan
			}
		}
		return plan
	}
}

func appendDebugCommand(command string) func(radar.Plan) radar.Plan {
	return appendDebugCommands(command)
}

func appendDebugCommands(commands ...string) func(radar.Plan) radar.Plan {
	return func(plan radar.Plan) radar.Plan {
		plan.ConfigurationCommands = append(plan.ConfigurationCommands, commands...)
		return plan
	}
}

func insertDebugCommandsBefore(name string, commands ...string) func(radar.Plan) radar.Plan {
	return func(plan radar.Plan) radar.Plan {
		for index, command := range plan.ConfigurationCommands {
			fields := strings.Fields(command)
			if len(fields) != 0 && fields[0] == name {
				result := make([]string, 0, len(plan.ConfigurationCommands)+len(commands))
				result = append(result, plan.ConfigurationCommands[:index]...)
				result = append(result, commands...)
				result = append(result, plan.ConfigurationCommands[index:]...)
				plan.ConfigurationCommands = result
				return plan
			}
		}
		return plan
	}
}
