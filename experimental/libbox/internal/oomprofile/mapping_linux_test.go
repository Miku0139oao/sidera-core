//go:build linux

package oomprofile

import (
	"encoding/binary"
	"errors"
	"testing"
)

func TestParseProcSelfMaps(t *testing.T) {
	data := []byte(
		"00400000-0040b000 r-xp 00000000 fc:01 787766 /bin/cat\n" +
			"0060a000-0060b000 r--p 0000a000 fc:01 787766 /bin/cat\n" +
			"7f000000-7f001000 r-xp 00001000 fc:01 99 /tmp/app (deleted)\n" +
			"7f100000-7f101000 r-xp 00000000 fc:01 100 /opt/my app/bin\n" +
			"7ffc0000-7ffc1000 r-xp 00000000 00:00 0\n" +
			"7ffc1000-7ffc2000 r-xp 00000000 00:00 0 [vdso]\n",
	)
	var mappings []memMap
	parseProcSelfMaps(data, func(low, high, offset uint64, file, buildID string) {
		mappings = append(mappings, memMap{
			start:   uintptr(low),
			end:     uintptr(high),
			offset:  offset,
			file:    file,
			buildID: buildID,
		})
	})

	if len(mappings) != 4 {
		t.Fatalf("unexpected executable mapping count: got %d, want 4", len(mappings))
	}
	if mappings[0].start != 0x00400000 || mappings[0].end != 0x0040b000 || mappings[0].file != "/bin/cat" {
		t.Fatalf("unexpected first mapping: %+v", mappings[0])
	}
	if mappings[1].offset != 0x1000 || mappings[1].file != "/tmp/app" {
		t.Fatalf("deleted mapping marker was not normalized: %+v", mappings[1])
	}
	if mappings[2].file != "/opt/my app/bin" {
		t.Fatalf("path with spaces was not retained: %+v", mappings[2])
	}
	if mappings[3].file != "[vdso]" {
		t.Fatalf("inode-zero named mapping was not retained: %+v", mappings[3])
	}
}

func TestParseELFBuildIDNotes(t *testing.T) {
	for _, byteOrder := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		name := []byte{'G', 'N', 'U', 0}
		description := []byte{0xde, 0xad, 0xbe, 0xef, 0x01}
		data := make([]byte, 12+len(name)+8)
		byteOrder.PutUint32(data[0:4], uint32(len(name)))
		byteOrder.PutUint32(data[4:8], uint32(len(description)))
		byteOrder.PutUint32(data[8:12], 3)
		copy(data[12:], name)
		copy(data[16:], description)

		buildID, found, err := parseELFBuildIDNotes(data, byteOrder)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatal("GNU build ID note was not found")
		}
		if buildID != "deadbeef01" {
			t.Fatalf("unexpected build ID: %q", buildID)
		}
	}
}

func TestParseELFBuildIDNotesRejectsMalformedData(t *testing.T) {
	data := make([]byte, 12)
	binary.LittleEndian.PutUint32(data[0:4], 64)
	if _, _, err := parseELFBuildIDNotes(data, binary.LittleEndian); !errors.Is(err, errMalformedELF) {
		t.Fatalf("unexpected malformed-note error: %v", err)
	}
}

func TestParseELFBuildIDNotesSkipsNonGNUAndOddNameSize(t *testing.T) {
	note := func(name []byte, noteType uint32, description []byte) []byte {
		nameSize := uint32(len(name))
		alignedName := (nameSize + 3) &^ 3
		alignedDesc := (uint32(len(description)) + 3) &^ 3
		data := make([]byte, 12+alignedName+alignedDesc)
		binary.LittleEndian.PutUint32(data[0:4], nameSize)
		binary.LittleEndian.PutUint32(data[4:8], uint32(len(description)))
		binary.LittleEndian.PutUint32(data[8:12], noteType)
		copy(data[12:], name)
		copy(data[12+alignedName:], description)
		return data
	}
	data := append(note([]byte("XXX\x00"), 3, []byte{0xaa}), note([]byte("GNU\x00"), 3, []byte{0x11, 0x22})...)
	buildID, found, err := parseELFBuildIDNotes(data, binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	if !found || buildID != "1122" {
		t.Fatalf("expected later GNU note, got found=%v id=%q", found, buildID)
	}
	odd := note([]byte("GNU\x00\x00"), 3, []byte{0x33})
	_, found, err = parseELFBuildIDNotes(odd, binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("nonstandard namesz should not match a GNU build ID")
	}
}
