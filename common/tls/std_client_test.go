package tls

import "testing"

func TestVerifyPublicKeySHA256RejectsMissingCertificate(t *testing.T) {
	if err := VerifyPublicKeySHA256([][]byte{make([]byte, 32)}, nil); err == nil {
		t.Fatal("accepted an empty peer certificate chain")
	}
}
