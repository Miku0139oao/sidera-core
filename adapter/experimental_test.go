package adapter

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/sagernet/sing/common/varbin"

	"github.com/stretchr/testify/require"
)

func TestSavedBinaryRoundTripWithURLHash(t *testing.T) {
	t.Parallel()
	saved := &SavedBinary{
		Content:     []byte("rule-set-content"),
		LastUpdated: time.Unix(1700000000, 0),
		LastEtag:    `"etag"`,
		URLHash:     []byte{1, 2, 3, 4},
	}
	encoded, err := saved.MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, uint8(2), encoded[0])
	var decoded SavedBinary
	require.NoError(t, decoded.UnmarshalBinary(encoded))
	require.Equal(t, saved.Content, decoded.Content)
	require.Equal(t, saved.LastUpdated, decoded.LastUpdated)
	require.Equal(t, saved.LastEtag, decoded.LastEtag)
	require.Equal(t, saved.URLHash, decoded.URLHash)
}

func TestSavedBinaryUnmarshalVersion1(t *testing.T) {
	t.Parallel()
	var buffer bytes.Buffer
	require.NoError(t, binary.Write(&buffer, binary.BigEndian, uint8(1)))
	_, err := varbin.WriteUvarint(&buffer, uint64(len("content")))
	require.NoError(t, err)
	_, err = buffer.WriteString("content")
	require.NoError(t, err)
	require.NoError(t, binary.Write(&buffer, binary.BigEndian, int64(1700000000)))
	_, err = varbin.WriteUvarint(&buffer, uint64(len("etag")))
	require.NoError(t, err)
	_, err = buffer.WriteString("etag")
	require.NoError(t, err)

	var decoded SavedBinary
	require.NoError(t, decoded.UnmarshalBinary(buffer.Bytes()))
	require.Equal(t, []byte("content"), decoded.Content)
	require.Equal(t, "etag", decoded.LastEtag)
	require.Nil(t, decoded.URLHash)
}

func TestSavedBinaryUnmarshalRejectsOversizedLengths(t *testing.T) {
	t.Parallel()
	t.Run("content", func(t *testing.T) {
		t.Parallel()
		var buffer bytes.Buffer
		require.NoError(t, binary.Write(&buffer, binary.BigEndian, uint8(1)))
		_, err := varbin.WriteUvarint(&buffer, 1<<40)
		require.NoError(t, err)
		var decoded SavedBinary
		require.Error(t, decoded.UnmarshalBinary(buffer.Bytes()))
	})
	t.Run("etag", func(t *testing.T) {
		t.Parallel()
		var buffer bytes.Buffer
		require.NoError(t, binary.Write(&buffer, binary.BigEndian, uint8(1)))
		_, err := varbin.WriteUvarint(&buffer, 0)
		require.NoError(t, err)
		require.NoError(t, binary.Write(&buffer, binary.BigEndian, int64(0)))
		_, err = varbin.WriteUvarint(&buffer, 1<<40)
		require.NoError(t, err)
		var decoded SavedBinary
		require.Error(t, decoded.UnmarshalBinary(buffer.Bytes()))
	})
	t.Run("url_hash", func(t *testing.T) {
		t.Parallel()
		var buffer bytes.Buffer
		require.NoError(t, binary.Write(&buffer, binary.BigEndian, uint8(2)))
		_, err := varbin.WriteUvarint(&buffer, 0)
		require.NoError(t, err)
		require.NoError(t, binary.Write(&buffer, binary.BigEndian, int64(0)))
		_, err = varbin.WriteUvarint(&buffer, 0)
		require.NoError(t, err)
		_, err = varbin.WriteUvarint(&buffer, 1<<40)
		require.NoError(t, err)
		var decoded SavedBinary
		require.Error(t, decoded.UnmarshalBinary(buffer.Bytes()))
	})
}
