//go:build linux

package oomprofile

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
)

var (
	procMapsSpace   = []byte(" ")
	procMapsNewline = []byte("\n")
	errMalformedELF = errors.New("malformed ELF note section")
	errNoELFBuildID = errors.New("no NT_GNU_BUILD_ID found in ELF binary")
	gnuBuildIDName  = []byte("GNU")
	deletedFileMark = []byte(" (deleted)")
)

func (b *profileBuilder) readMapping() {
	data, _ := os.ReadFile("/proc/self/maps")
	parseProcSelfMaps(data, func(lo, hi, offset uint64, file, buildID string) {
		b.addMappingEntry(lo, hi, offset, file, buildID, false)
	})
	if len(b.mem) == 0 {
		b.addMappingEntry(0, 0, 0, "", "", true)
	}
}

// parseProcSelfMaps is a local copy of runtime/pprof's executable-mapping parser.
func parseProcSelfMaps(data []byte, addMapping func(lo, hi, offset uint64, file, buildID string)) {
	var line []byte
	next := func() []byte {
		var field []byte
		field, line, _ = bytes.Cut(line, procMapsSpace)
		line = bytes.TrimLeft(line, " ")
		return field
	}

	for len(data) > 0 {
		line, data, _ = bytes.Cut(data, procMapsNewline)
		address := next()
		lowString, highString, ok := strings.Cut(string(address), "-")
		if !ok {
			continue
		}
		low, err := strconv.ParseUint(lowString, 16, 64)
		if err != nil {
			continue
		}
		high, err := strconv.ParseUint(highString, 16, 64)
		if err != nil {
			continue
		}
		permissions := next()
		if len(permissions) < 4 || permissions[2] != 'x' {
			continue
		}
		offset, err := strconv.ParseUint(string(next()), 16, 64)
		if err != nil {
			continue
		}
		next()          // device
		inode := next() // inode
		if line == nil {
			continue
		}
		file := bytes.TrimSuffix(line, deletedFileMark)
		if len(inode) == 1 && inode[0] == '0' && len(file) == 0 {
			continue
		}

		buildID, _ := elfBuildID(string(file))
		addMapping(low, high, offset, string(file), buildID)
	}
}

// elfBuildID is a local copy of runtime/pprof's GNU build-ID reader.
func elfBuildID(path string) (string, error) {
	file, err := elf.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	for _, section := range file.Sections {
		if section.Type != elf.SHT_NOTE {
			continue
		}
		data, err := section.Data()
		if err != nil {
			return "", err
		}
		buildID, found, err := parseELFBuildIDNotes(data, file.ByteOrder)
		if err != nil {
			return "", err
		}
		if found {
			return buildID, nil
		}
	}
	return "", errNoELFBuildID
}

func parseELFBuildIDNotes(data []byte, byteOrder binary.ByteOrder) (string, bool, error) {
	const (
		noteHeaderSize = uint64(12)
		gnuBuildIDType = uint32(3)
	)
	align := func(size uint64) uint64 {
		return (size + 3) &^ 3
	}

	for len(data) > 0 {
		if uint64(len(data)) < noteHeaderSize {
			return "", false, errMalformedELF
		}
		nameSize := uint64(byteOrder.Uint32(data[0:4]))
		descriptionSize := uint64(byteOrder.Uint32(data[4:8]))
		noteType := byteOrder.Uint32(data[8:12])
		descriptionStart := noteHeaderSize + align(nameSize)
		descriptionEnd := descriptionStart + descriptionSize
		recordEnd := descriptionStart + align(descriptionSize)
		if descriptionEnd > uint64(len(data)) || recordEnd > uint64(len(data)) {
			return "", false, errMalformedELF
		}

		name := bytes.TrimRight(data[noteHeaderSize:noteHeaderSize+nameSize], "\x00")
		if noteType == gnuBuildIDType && nameSize == 4 && bytes.Equal(name, gnuBuildIDName) {
			return hex.EncodeToString(data[descriptionStart:descriptionEnd]), true, nil
		}
		data = data[recordEnd:]
	}
	return "", false, nil
}
