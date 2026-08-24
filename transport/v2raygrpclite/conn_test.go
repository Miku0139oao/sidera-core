package v2raygrpclite

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestGunConnFrameRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("frame"), 100)
	var wire bytes.Buffer
	writer := newGunConn(bytes.NewReader(nil), &wire, nil)
	written, err := writer.Write(payload)
	if err != nil {
		t.Fatal(err)
	}
	if written != len(payload) {
		t.Fatalf("written = %d, want %d", written, len(payload))
	}

	reader := newGunConn(bytes.NewReader(wire.Bytes()), io.Discard, nil)
	decoded := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, decoded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatal("decoded payload mismatch")
	}
}

func TestGunConnSkipsEmptyFrames(t *testing.T) {
	var wire bytes.Buffer
	writer := newGunConn(bytes.NewReader(nil), &wire, nil)
	if _, err := writer.Write(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	reader := newGunConn(bytes.NewReader(wire.Bytes()), io.Discard, nil)
	decoded := make([]byte, len("payload"))
	if _, err := io.ReadFull(reader, decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "payload" {
		t.Fatalf("decoded = %q", decoded)
	}
}

func TestGunConnRejectsInvalidFrameLength(t *testing.T) {
	var wire bytes.Buffer
	wire.Write([]byte{0, 0, 0, 0, 1, 0x0A})
	wire.WriteByte(1)
	wire.WriteByte('x')
	reader := newGunConn(bytes.NewReader(wire.Bytes()), io.Discard, nil)
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("accepted an inconsistent gRPC frame length")
	}
}

func TestGunConnRejectsOversizedVarint(t *testing.T) {
	var wire bytes.Buffer
	wire.Write([]byte{0, 0xff, 0xff, 0xff, 0xff, 0x0A})
	var encoded [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(encoded[:], ^uint64(0))
	wire.Write(encoded[:n])
	reader := newGunConn(bytes.NewReader(wire.Bytes()), io.Discard, nil)
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("accepted an oversized gRPC frame")
	}
}
