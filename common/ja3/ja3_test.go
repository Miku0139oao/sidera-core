package ja3

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestIsGREASETruthTable(t *testing.T) {
	for value := 0; value <= 0xffff; value++ {
		v := uint16(value)
		want := byte(v>>8) == byte(v) && byte(v)&0x0f == 0x0a
		if got := IsGREASE(v); got != want {
			t.Fatalf("IsGREASE(%#04x) = %v, want %v", v, got, want)
		}
	}
}

func TestMarshalJA3ExcludesOnlyGrease(t *testing.T) {
	hello := &ClientHello{
		Version:         771,
		CipherSuites:    []uint16{0x0a0a, 0x1a2a, 4865},
		Extensions:      []uint16{0x1a1a, 0},
		EllipticCurves:  []uint16{0x2a2a, 29},
		EllipticCurvePF: []uint8{0},
	}
	if got, want := hello.String(), "771,6698-4865,0,29,0"; got != want {
		t.Fatalf("JA3 string = %q, want %q", got, want)
	}
}

func TestMarshalJA3AllGreaseKeepsFieldDelimiters(t *testing.T) {
	hello := &ClientHello{
		Version:         771,
		CipherSuites:    []uint16{0x0a0a},
		Extensions:      []uint16{0x1a1a},
		EllipticCurves:  []uint16{0x2a2a},
		EllipticCurvePF: []uint8{0},
	}
	if got, want := hello.String(), "771,,,,0"; got != want {
		t.Fatalf("JA3 string = %q, want %q", got, want)
	}
}

func TestEqualsCanIgnoreExtensionOrder(t *testing.T) {
	left := &ClientHello{Version: 771, Extensions: []uint16{1, 2, 3}}
	right := &ClientHello{Version: 771, Extensions: []uint16{3, 1, 2}}
	if left.Equals(right, false) {
		t.Fatal("ordered comparison ignored extension order")
	}
	if !left.Equals(right, true) {
		t.Fatal("order-insensitive comparison did not sort both operands")
	}
}

func TestComputeRejectsMalformedUint16Vectors(t *testing.T) {
	t.Run("cipher_suites", func(t *testing.T) {
		for length := 0; length <= 3; length++ {
			payload := makeClientHello(bytes.Repeat([]byte{0x13}, length))
			_, err := Compute(payload)
			if (length == 2) != (err == nil) {
				t.Fatalf("cipher vector length %d: error = %v", length, err)
			}
		}
	})

	tests := []struct {
		name      string
		extension uint16
		prefixLen int
	}{
		{name: "supported_groups", extension: ecExtensionType, prefixLen: 2},
		{name: "supported_versions", extension: versionExtensionType, prefixLen: 1},
		{name: "signature_algorithms", extension: signatureAlgorithmsExtensionType, prefixLen: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for length := 0; length <= 3; length++ {
				body := make([]byte, test.prefixLen+length)
				if test.prefixLen == 1 {
					body[0] = byte(length)
				} else {
					binary.BigEndian.PutUint16(body, uint16(length))
				}
				payload := makeClientHello([]byte{0x13, 0x01}, makeExtension(test.extension, body))
				_, err := Compute(payload)
				if (length == 2) != (err == nil) {
					t.Fatalf("vector length %d: error = %v", length, err)
				}
			}
		})
	}
}

func TestComputeRejectsMalformedServerName(t *testing.T) {
	tests := [][]byte{
		{0, 3, 0, 0, 0},      // empty host_name
		{0, 4, 0, 0, 2, 'a'}, // name length exceeds its entry
		{0, 3, 0, 0, 1, 'a'}, // list length does not consume the extension
		{0, 4, 1, 0, 1, 'a'}, // unsupported name type
	}
	for index, body := range tests {
		if _, err := Compute(makeClientHello([]byte{0x13, 0x01}, makeExtension(sniExtensionType, body))); err == nil {
			t.Fatalf("malformed SNI case %d was accepted", index)
		}
	}
}

func FuzzCompute(f *testing.F) {
	f.Add(makeClientHello([]byte{0x13, 0x01}, makeExtension(ecExtensionType, []byte{0, 2, 0, 29})))
	f.Add([]byte(nil))
	f.Add([]byte{22, 3, 1, 0, 1, 1})
	f.Fuzz(func(t *testing.T, payload []byte) {
		_, _ = Compute(payload)
	})
}

func makeExtension(extensionType uint16, body []byte) []byte {
	extension := make([]byte, 4, 4+len(body))
	binary.BigEndian.PutUint16(extension, extensionType)
	binary.BigEndian.PutUint16(extension[2:], uint16(len(body)))
	return append(extension, body...)
}

func makeClientHello(cipherSuites []byte, extensions ...[]byte) []byte {
	body := make([]byte, 0, 64)
	body = append(body, 0x03, 0x03)
	body = append(body, make([]byte, 32)...)
	body = append(body, 0) // session ID
	body = binary.BigEndian.AppendUint16(body, uint16(len(cipherSuites)))
	body = append(body, cipherSuites...)
	body = append(body, 1, 0) // one null compression method
	if extensions != nil {
		var extensionBody []byte
		for _, extension := range extensions {
			extensionBody = append(extensionBody, extension...)
		}
		body = binary.BigEndian.AppendUint16(body, uint16(len(extensionBody)))
		body = append(body, extensionBody...)
	}

	handshake := []byte{handshakeType, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}
	handshake = append(handshake, body...)
	record := []byte{contentType, 0x03, 0x01}
	record = binary.BigEndian.AppendUint16(record, uint16(len(handshake)))
	return append(record, handshake...)
}
