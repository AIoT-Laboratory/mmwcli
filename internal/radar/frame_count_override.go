package radar

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// OverrideCaptureSessionV1FrameCount returns the exact effective CFG snapshot
// for a capture. It rewrites the sole legacy frameCfg count while preserving
// all other lines and validates the resulting capture-session contract.
func OverrideCaptureSessionV1FrameCount(snapshot []byte, frameCount uint16) ([]byte, error) {
	if !utf8.Valid(snapshot) {
		return nil, errors.New("capture session v1 CFG must be valid UTF-8")
	}

	lines := strings.SplitAfter(string(snapshot), "\n")
	found := 0
	for index, rawLine := range lines {
		body, ending := splitConfigLineEnding(rawLine)
		leftTrimmed := strings.TrimLeft(body, " \t")
		indent := body[:len(body)-len(leftTrimmed)]
		command, comment := splitInlineSlashComment(leftTrimmed)
		fields := strings.Fields(command)
		if len(fields) == 0 || fields[0] != "frameCfg" {
			continue
		}
		found++
		if len(fields) != 8 {
			return nil, fmt.Errorf("legacy frameCfg must contain seven arguments: %s", strings.TrimSpace(command))
		}
		fields[4] = strconv.FormatUint(uint64(frameCount), 10)
		lines[index] = indent + strings.Join(fields, " ") + comment + ending
	}
	if found != 1 {
		return nil, fmt.Errorf("capture configuration must contain exactly one legacy frameCfg; found %d", found)
	}

	effective := []byte(strings.Join(lines, ""))
	if _, err := BuildCaptureSessionV1Plan(effective, FullConfiguration); err != nil {
		return nil, fmt.Errorf("validate frame-count override: %w", err)
	}
	return effective, nil
}

func splitConfigLineEnding(line string) (body, ending string) {
	if strings.HasSuffix(line, "\r\n") {
		return strings.TrimSuffix(line, "\r\n"), "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return strings.TrimSuffix(line, "\n"), "\n"
	}
	return line, ""
}

func splitInlineSlashComment(line string) (command, comment string) {
	if index := strings.Index(line, " //"); index >= 0 {
		return line[:index], line[index:]
	}
	return line, ""
}
