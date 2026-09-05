package xrayencryption

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
)

func BenchmarkXorConnWriteSmall(b *testing.B) {
	benchmarkXorConnWrite(b, xorBenchRecords(b, 1, 17))
}

func BenchmarkXorConnWrite16KiB(b *testing.B) {
	benchmarkXorConnWrite(b, xorBenchRecords(b, 1, 16384-5))
}

func BenchmarkXorConnWrite32KiB(b *testing.B) {
	benchmarkXorConnWrite(b, xorBenchRecords(b, 2, 16384-5))
}

func BenchmarkXorConnWriteOversize(b *testing.B) {
	payload := 16640
	count := buf.MaxPooledBufferSize/(5+payload) + 1
	benchmarkXorConnWrite(b, xorBenchRecords(b, count, payload))
}

func benchmarkXorConnWrite(b *testing.B, payload []byte) {
	b.Helper()
	key := bytes.Repeat([]byte{1}, 32)
	iv := bytes.Repeat([]byte{2}, 16)
	conn := NewXorConn(xorBenchDiscard{}, NewCTR(key, iv), nil, 0, 0)
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := conn.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
}

func xorBenchRecords(b *testing.B, count, payload int) []byte {
	b.Helper()
	record := make([]byte, 5+payload)
	EncodeHeader(record, payload)
	out := make([]byte, 0, count*len(record))
	for range count {
		out = append(out, record...)
	}
	return out
}

type xorBenchDiscard struct{}

func (xorBenchDiscard) Write(p []byte) (int, error) { return len(p), nil }
func (xorBenchDiscard) Read([]byte) (int, error)    { return 0, io.EOF }
func (xorBenchDiscard) Close() error                { return nil }
func (xorBenchDiscard) LocalAddr() net.Addr         { return testAddr("local") }
func (xorBenchDiscard) RemoteAddr() net.Addr        { return testAddr("remote") }
func (xorBenchDiscard) SetDeadline(time.Time) error { return nil }
func (xorBenchDiscard) SetReadDeadline(time.Time) error {
	return nil
}
func (xorBenchDiscard) SetWriteDeadline(time.Time) error { return nil }
