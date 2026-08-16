//go:build windows

package main

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestReadCurrentProcessValue(t *testing.T) {
	source := uint64(0x0123456789abcdef)
	var destination uint64

	err := readCurrentProcessValue(uintptr(unsafe.Pointer(&source)), &destination)
	runtime.KeepAlive(&source)
	if err != nil {
		t.Fatal(err)
	}
	if destination != source {
		t.Fatalf("unexpected copied value: got %#x, want %#x", destination, source)
	}
}

func TestReadCurrentProcessValueRejectsNullAddress(t *testing.T) {
	var destination uint64
	if err := readCurrentProcessValue(0, &destination); err == nil {
		t.Fatal("expected a null-address read to fail")
	}
}

func TestReadCurrentProcessBytes(t *testing.T) {
	source := []byte("authenticode-cert")
	copied, err := readCurrentProcessBytes(uintptr(unsafe.Pointer(&source[0])), uint32(len(source)))
	runtime.KeepAlive(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != string(source) {
		t.Fatalf("unexpected copied bytes: got %q, want %q", copied, source)
	}
	if _, err = readCurrentProcessBytes(0, 4); err == nil {
		t.Fatal("expected a null-address byte read to fail")
	}
}
