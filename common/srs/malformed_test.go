package srs

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"testing"

	C "github.com/Miku0139oao/sidera-core/constant"

	"github.com/stretchr/testify/require"
)

func craftRuleSet(body []byte) []byte {
	var buffer bytes.Buffer
	buffer.Write(MagicBytes[:])
	buffer.WriteByte(C.RuleSetVersionCurrent)
	compressWriter := zlib.NewWriter(&buffer)
	_, _ = compressWriter.Write(body)
	_ = compressWriter.Close()
	return buffer.Bytes()
}

func uvarint(value uint64) []byte {
	return binary.AppendUvarint(nil, value)
}

// TestReadMalformed ensures crafted counts and lengths from untrusted input
// cannot trigger large upfront allocations or unbounded logical-rule recursion.
func TestReadMalformed(t *testing.T) {
	t.Parallel()
	defaultRule := []byte{0x00}
	cases := []struct {
		name string
		body []byte
	}{
		{"huge_rule_count", uvarint(1 << 40)},
		{"huge_string_count", bytes.Join([][]byte{uvarint(1), defaultRule, {ruleItemDomainKeyword}, uvarint(1 << 40)}, nil)},
		{"huge_string_length", bytes.Join([][]byte{uvarint(1), defaultRule, {ruleItemDomainKeyword}, uvarint(1), uvarint(1 << 40)}, nil)},
		{"huge_uint16_count", bytes.Join([][]byte{uvarint(1), defaultRule, {ruleItemQueryType}, uvarint(1 << 40)}, nil)},
		{"huge_uint8_count", bytes.Join([][]byte{uvarint(1), defaultRule, {ruleItemNetworkType}, uvarint(1 << 40)}, nil)},
		{"deep_logical_nesting", bytes.Join([][]byte{uvarint(1), bytes.Repeat([]byte{0x01, 0x00, 0x01}, 10000)}, nil)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := Read(bytes.NewReader(craftRuleSet(testCase.body)), false)
			require.Error(t, err)
		})
	}
}
