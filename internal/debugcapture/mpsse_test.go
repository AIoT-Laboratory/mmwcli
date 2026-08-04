package debugcapture

import (
	"strings"
	"testing"
)

func TestSPIMPSSECommands(t *testing.T) {
	if got, want := spiMPSSEInitCommand(), [9]byte{0x8A, 0x97, 0x8D, 0x86, 0x02, 0x00, 0x80, 0xC8, 0x4B}; got != want {
		t.Fatalf("SPI init = % X, want % X", got, want)
	}
	if got, want := spiMPSSEWriteWord(0x1234), [12]byte{0x80, 0xC0, 0x4B, 0x11, 0x01, 0x00, 0x12, 0x34, 0x80, 0xC8, 0x4B, 0x87}; got != want {
		t.Fatalf("SPI write = % X, want % X", got, want)
	}
	if got, want := spiMPSSEReadWordCommand(), [10]byte{0x80, 0xC2, 0x4B, 0x20, 0x01, 0x00, 0x80, 0xC8, 0x4B, 0x87}; got != want {
		t.Fatalf("SPI read = % X, want % X", got, want)
	}
}

func TestDecodeSPIWord(t *testing.T) {
	word, err := decodeSPIWord([]byte{0x12, 0x34})
	if err != nil {
		t.Fatal(err)
	}
	if word != 0x1234 {
		t.Fatalf("word = 0x%04X, want 0x1234", word)
	}

	_, err = decodeSPIWord([]byte{0x12})
	if err == nil || !strings.Contains(err.Error(), "want 2") {
		t.Fatalf("short response error = %v", err)
	}
}

func TestIRQMPSSECommands(t *testing.T) {
	if got, want := irqMPSSESyncCommand(), [1]byte{0xAB}; got != want {
		t.Fatalf("IRQ sync = % X, want % X", got, want)
	}
	if !validIRQMPSSESync([]byte{0xFA, 0xAB}) {
		t.Fatal("valid IRQ sync response rejected")
	}
	for _, response := range [][]byte{{0xFA}, {0xFA, 0xAA}, {0x00, 0xFA, 0xAB}} {
		if validIRQMPSSESync(response) {
			t.Fatalf("invalid IRQ sync response accepted: % X", response)
		}
	}
	if got, want := irqMPSSEClockCommand(), [3]byte{0x8A, 0x97, 0x8C}; got != want {
		t.Fatalf("IRQ clock = % X, want % X", got, want)
	}
	if got, want := irqMPSSEGPIOCommand(), [6]byte{0x80, 0x13, 0x5B, 0x86, 0x4A, 0x00}; got != want {
		t.Fatalf("IRQ GPIO = % X, want % X", got, want)
	}
	if got, want := irqMPSSELoopbackOffCommand(), [1]byte{0x85}; got != want {
		t.Fatalf("IRQ loopback = % X, want % X", got, want)
	}
	if got, want := irqMPSSEReadCommand(), [1]byte{0x81}; got != want {
		t.Fatalf("IRQ read = % X, want % X", got, want)
	}
}

func TestIRQAsserted(t *testing.T) {
	if !irqAsserted(0x20) || !irqAsserted(0xFF) {
		t.Fatal("asserted IRQ not detected")
	}
	if irqAsserted(0x00) || irqAsserted(0x10) {
		t.Fatal("inactive IRQ reported as asserted")
	}
}
