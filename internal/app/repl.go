package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"mmwcli/internal/radar"
	"mmwcli/internal/serialport"
)

const (
	maximumREPLCommandBytes = 254
	maximumREPLInputBytes   = 4096
)

type replClient interface {
	VerifyPlatformContext(context.Context) (string, error)
	SendCommandContext(context.Context, string) (string, error)
	Close() error
}

type replClientOpener func(string, int, time.Duration) (replClient, error)

func openStudioREPLClient(port string, baud int, timeout time.Duration) (replClient, error) {
	transport, err := serialport.Open(port, baud, timeout)
	if err != nil {
		return nil, err
	}
	client, err := radar.NewClient(transport, radar.StudioCLI, timeout)
	if err != nil {
		return nil, errors.Join(err, transport.Close())
	}
	return client, nil
}

func runREPL(
	arguments []string,
	input io.Reader,
	stdout, stderr io.Writer,
	open replClientOpener,
) (resultErr error) {
	if len(arguments) != 0 && isHelp(arguments[0]) {
		printREPLHelp(stdout)
		return nil
	}
	flags := newCommandFlagSet(
		"repl",
		stderr,
		"mmwcli repl --port PORT [--baud 921600] [--serial-timeout-ms 10000]",
	)
	portName := flags.String("port", "", "serial port (COM3 or /dev/ttyACM0)")
	baud := flags.Int("baud", radar.StudioCLI.DefaultBaud(), "serial baud")
	timeoutMS := flags.Int("serial-timeout-ms", 10000, "serial command timeout")
	if err := parseCommandFlags(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return usageError{message: "unexpected repl arguments: " + strings.Join(flags.Args(), " ")}
	}
	if strings.TrimSpace(*portName) == "" {
		return usageError{message: "--port is required; mmwcli never scans serial ports"}
	}
	if *baud < 1200 || *baud > 4000000 {
		return usageError{message: "--baud must be in 1200..4000000"}
	}
	if *timeoutMS < 100 || *timeoutMS > 25500 {
		return usageError{message: "--serial-timeout-ms must be in 100..25500"}
	}
	if input == nil {
		return errors.New("repl input is nil")
	}

	client, err := open(*portName, *baud, time.Duration(*timeoutMS)*time.Millisecond)
	if err != nil {
		return err
	}
	if client == nil {
		return errors.New("repl opener returned a nil client")
	}
	defer func() {
		resultErr = errors.Join(resultErr, client.Close())
	}()

	verifyContext, stopVerify := hardwareSignalContext()
	verification, err := client.VerifyPlatformContext(verifyContext)
	stopVerify()
	if verification != "" {
		fmt.Fprint(stdout, verification)
	}
	if err != nil {
		return err
	}

	interactive := readerIsTerminal(input)
	if interactive {
		fmt.Fprintln(stderr, "studio-cli protocol verified; enter one command per line")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 256), maximumREPLInputBytes)
	for {
		if interactive {
			fmt.Fprint(stderr, "mmwcli> ")
		}
		if !scanner.Scan() {
			break
		}
		command, ok, err := parseREPLLine(scanner.Text())
		if err != nil {
			return err
		}
		if !ok {
			continue
		}

		commandContext, stopCommand := hardwareSignalContext()
		response, commandErr := client.SendCommandContext(commandContext, command)
		stopCommand()
		if response != "" {
			fmt.Fprint(stdout, response)
		}
		if commandErr == nil {
			continue
		}
		var explicitError *radar.CommandError
		if errors.As(commandErr, &explicitError) {
			fmt.Fprintln(stderr, explicitError)
			continue
		}
		return commandErr
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read repl input: %w", err)
	}
	return nil
}

func parseREPLLine(line string) (string, bool, error) {
	commands, err := radar.ParseConfig(strings.NewReader(line))
	if err != nil {
		return "", false, err
	}
	if len(commands) == 0 {
		return "", false, nil
	}
	command := commands[0]
	if strings.ContainsRune(command, 0) {
		return "", false, errors.New("repl command contains NUL")
	}
	if len(command) > maximumREPLCommandBytes {
		return "", false, fmt.Errorf(
			"repl command is %d bytes; studio-cli maximum is %d",
			len(command),
			maximumREPLCommandBytes,
		)
	}
	return command, true, nil
}

func readerIsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
