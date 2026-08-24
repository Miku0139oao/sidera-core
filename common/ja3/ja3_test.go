package ja3

import "testing"

func TestMarshalJA3ExcludesGrease(t *testing.T) {
	hello := &ClientHello{
		Version:         771,
		CipherSuites:    []uint16{0x0a0a, 4865},
		Extensions:      []uint16{0x1a1a, 0},
		EllipticCurves:  []uint16{0x2a2a, 29},
		EllipticCurvePF: []uint8{0},
	}
	if got, want := hello.String(), "771,4865,0,29,0"; got != want {
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
