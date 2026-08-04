package dca

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultOptionsBindWildcard4096(t *testing.T) {
	options := DefaultOptions()
	if options.ControlBindPort != 4096 || !options.ControlBindAddress.IsUnspecified() {
		t.Fatalf("default control bind = %s:%d, want 0.0.0.0:4096", options.ControlBindAddress, options.ControlBindPort)
	}
}

func TestClientAcceptsMatchingResponseFromAlternateSource(t *testing.T) {
	requests := listenUDP(t, net.IPv4(127, 0, 0, 1))
	responses := listenUDP(t, net.IPv4(127, 0, 0, 2))
	serverErr := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1024)
		length, sender, err := requests.ReadFromUDP(buffer)
		if err != nil {
			serverErr <- err
			return
		}
		command, payload, err := parseTestRequest(buffer[:length])
		if err != nil {
			serverErr <- err
			return
		}
		if command != CommandSystemAlive || len(payload) != 0 {
			serverErr <- errors.New("fake received unexpected alive request")
			return
		}
		noise := [][]byte{
			{1, 2, 3},
			append(testResponse(CommandSystemAlive, 9), 0),
			testResponse(CommandReadFPGAVersion, 0),
			testResponse(CommandAsyncStatus, 0x1234),
		}
		for _, datagram := range noise {
			if _, err := responses.WriteToUDP(datagram, sender); err != nil {
				serverErr <- err
				return
			}
		}
		_, err = responses.WriteToUDP(testResponse(CommandSystemAlive, 0), sender)
		serverErr <- err
	}()

	options := DefaultOptions()
	options.DeviceAddress = net.IPv4(127, 0, 0, 1)
	options.ControlPort = requests.LocalAddr().(*net.UDPAddr).Port
	options.ControlBindPort = 0
	options.Timeout = time.Second
	client, err := Dial(options)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	response, err := client.Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != 0 || response.Source == nil {
		t.Fatalf("unexpected response: %+v", response)
	}
	wantSource := responses.LocalAddr().(*net.UDPAddr)
	if !response.Source.IP.Equal(wantSource.IP) || response.Source.Port != wantSource.Port {
		t.Fatalf("response source = %v, want %v", response.Source, wantSource)
	}
	last := client.LastResponseEndpoint()
	if last == nil || !last.IP.Equal(wantSource.IP) || last.Port != wantSource.Port {
		t.Fatalf("last response source = %v, want %v", last, wantSource)
	}
	statuses := client.TakeAsyncStatuses()
	if len(statuses) != 1 || statuses[0].Status != 0x1234 || statuses[0].Command != CommandAsyncStatus {
		t.Fatalf("async statuses = %+v", statuses)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestClientConfigureSendsAuditedPayloads(t *testing.T) {
	server := listenUDP(t, net.IPv4(127, 0, 0, 1))
	serverErr := make(chan error, 1)
	go func() {
		want := []struct {
			command Command
			payload []byte
		}{
			{CommandConfigureFPGA, []byte{1, 2, 1, 2, 3, 30}},
			{CommandConfigureRecord, []byte{0xBE, 0x05, 0x35, 0x0C, 0, 0}},
		}
		buffer := make([]byte, 1024)
		for _, expected := range want {
			length, sender, err := server.ReadFromUDP(buffer)
			if err != nil {
				serverErr <- err
				return
			}
			command, payload, err := parseTestRequest(buffer[:length])
			if err != nil {
				serverErr <- err
				return
			}
			if command != expected.command || !bytes.Equal(payload, expected.payload) {
				serverErr <- errors.New("fake received unexpected configuration request")
				return
			}
			if _, err := server.WriteToUDP(testResponse(command, 0), sender); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()

	client := dialTestClient(t, server, time.Second)
	defer client.Close()
	if _, err := client.Configure(context.Background(), DefaultFPGAConfig(), 25); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestClientDrainsAsyncStatusesToQuietWindow(t *testing.T) {
	sender := listenUDP(t, net.IPv4(127, 0, 0, 2))
	server := listenUDP(t, net.IPv4(127, 0, 0, 1))
	client := dialTestClient(t, server, time.Second)
	defer client.Close()
	destination := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: client.LocalEndpoint().Port}
	if _, err := sender.WriteToUDP(testResponse(CommandAsyncStatus, 8), destination); err != nil {
		t.Fatal(err)
	}
	contextWithLimit, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	statuses, err := client.DrainAsyncStatuses(contextWithLimit, 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Status != 8 || statuses[0].Source == nil {
		t.Fatalf("drained statuses = %+v", statuses)
	}
	if statuses[0].Source.Port != sender.LocalAddr().(*net.UDPAddr).Port {
		t.Fatalf("async source = %v, want %v", statuses[0].Source, sender.LocalAddr())
	}
	if remaining := client.TakeAsyncStatuses(); len(remaining) != 0 {
		t.Fatalf("drain did not clear queue: %+v", remaining)
	}
}

func TestStartTimeoutSendsOneStopAndNeverRetriesStart(t *testing.T) {
	server := listenUDP(t, net.IPv4(127, 0, 0, 1))
	var starts atomic.Int32
	var stops atomic.Int32
	serverErr := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1024)
		deadline := time.Now().Add(2 * time.Second)
		for stops.Load() == 0 {
			if err := server.SetReadDeadline(deadline); err != nil {
				serverErr <- err
				return
			}
			length, sender, err := server.ReadFromUDP(buffer)
			if err != nil {
				serverErr <- err
				return
			}
			command, _, err := parseTestRequest(buffer[:length])
			if err != nil {
				serverErr <- err
				return
			}
			switch command {
			case CommandStartRecord:
				starts.Add(1) // Deliberately do not reply.
			case CommandStopRecord:
				stops.Add(1)
				if _, err := server.WriteToUDP(testResponse(command, 0), sender); err != nil {
					serverErr <- err
					return
				}
			default:
				serverErr <- errors.New("fake received unexpected command")
				return
			}
		}
		serverErr <- nil
	}()

	client := dialTestClient(t, server, 80*time.Millisecond)
	defer client.Close()
	_, err := client.StartRecordConvergent(context.Background())
	if err == nil {
		t.Fatal("StartRecordConvergent succeeded despite a dropped start reply")
	}
	if !errors.Is(err, ErrCommandTimeout) {
		t.Fatalf("error = %v, want ErrCommandTimeout", err)
	}
	var convergenceError *StartConvergenceError
	if !errors.As(err, &convergenceError) || convergenceError.StopErr != nil {
		t.Fatalf("unexpected convergence error: %#v", convergenceError)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if starts.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("requests: start=%d stop=%d, want exactly one each", starts.Load(), stops.Load())
	}
}

func listenUDP(t *testing.T, address net.IP) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: address, Port: 0})
	if err != nil {
		t.Fatalf("listen UDP on %s: %v", address, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func dialTestClient(t *testing.T, server *net.UDPConn, timeout time.Duration) *Client {
	t.Helper()
	serverEndpoint := server.LocalAddr().(*net.UDPAddr)
	options := DefaultOptions()
	options.DeviceAddress = serverEndpoint.IP
	options.ControlPort = serverEndpoint.Port
	options.ControlBindPort = 0
	options.Timeout = timeout
	client, err := Dial(options)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func parseTestRequest(packet []byte) (Command, []byte, error) {
	if len(packet) < ControlResponseSize {
		return 0, nil, errors.New("short request")
	}
	payloadLength := int(binary.LittleEndian.Uint16(packet[4:6]))
	if len(packet) != ControlResponseSize+payloadLength ||
		binary.LittleEndian.Uint16(packet[0:2]) != ControlHeader ||
		binary.LittleEndian.Uint16(packet[len(packet)-2:]) != ControlFooter {
		return 0, nil, errors.New("invalid request frame")
	}
	return Command(binary.LittleEndian.Uint16(packet[2:4])), append([]byte(nil), packet[6:6+payloadLength]...), nil
}
