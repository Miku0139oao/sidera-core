//go:build windows

package main

import (
	"bytes"
	"crypto/x509"
	"time"
	"unsafe"

	E "github.com/sagernet/sing/common/exceptions"

	"golang.org/x/sys/windows"
)

//go:generate go run golang.org/x/sys/windows/mkwinsyscall -output zsyscall_authenticode_windows.go authenticode_windows.go

//sys winTrustProviderDataFromStateData(stateData windows.Handle) (providerData uintptr) = wintrust.WTHelperProvDataFromStateData
//sys winTrustProviderSignerFromChain(providerData uintptr, signerIndex uint32, counterSigner uint32, counterSignerIndex uint32) (providerSigner uintptr) = wintrust.WTHelperGetProvSignerFromChain
//sys winTrustProviderCertificateFromChain(providerSigner uintptr, certificateIndex uint32) (providerCertificate uintptr) = wintrust.WTHelperGetProvCertFromChain

type cryptProviderCertificate struct {
	_                  uint32
	certificateContext *windows.CertContext
}

func readCurrentProcessValue[T any](address uintptr, value *T) error {
	size := unsafe.Sizeof(*value)
	buffer, err := readCurrentProcessBytes(address, uint32(size))
	if err != nil {
		return err
	}
	copy(unsafe.Slice((*byte)(unsafe.Pointer(value)), int(size)), buffer)
	return nil
}

func readCurrentProcessBytes(address uintptr, size uint32) ([]byte, error) {
	if address == 0 || size == 0 {
		return nil, E.New("empty process memory range")
	}
	buffer := make([]byte, size)
	var bytesRead uintptr
	err := windows.ReadProcessMemory(
		windows.CurrentProcess(),
		address,
		&buffer[0],
		uintptr(size),
		&bytesRead,
	)
	if err != nil {
		return nil, err
	}
	if bytesRead != uintptr(size) {
		return nil, E.New("short process memory read: ", bytesRead, " of ", size, " bytes")
	}
	return buffer, nil
}

func authenticodeSigner(path string, file windows.Handle) ([]byte, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	fileInformation := windows.WinTrustFileInfo{
		Size:     uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})),
		FilePath: pathPointer,
		File:     file,
	}
	trustData := windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&fileInformation),
		ProvFlags: windows.WTD_CACHE_ONLY_URL_RETRIEVAL |
			windows.WTD_REVOCATION_CHECK_NONE |
			windows.WTD_DISABLE_MD2_MD4,
		UIContext: windows.WTD_UICONTEXT_EXECUTE,
	}
	trustError := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &trustData)
	if trustError != nil && !E.IsMulti(
		trustError,
		windows.Errno(windows.CERT_E_UNTRUSTEDROOT),
		windows.Errno(windows.CERT_E_CHAINING),
	) {
		trustData.StateAction = windows.WTD_STATEACTION_CLOSE
		windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &trustData)
		return nil, E.Cause(trustError, "verify Authenticode signature")
	}
	signer, signerError := verifiedSignerCertificate(trustData.StateData)
	trustData.StateAction = windows.WTD_STATEACTION_CLOSE
	closeError := windows.WinVerifyTrustEx(windows.InvalidHWND, &windows.WINTRUST_ACTION_GENERIC_VERIFY_V2, &trustData)
	if signerError != nil {
		return nil, signerError
	}
	if closeError != nil {
		return nil, E.Cause(closeError, "close Authenticode verification")
	}
	certificate, err := validateCodeSigningCertificate(signer)
	if err != nil {
		return nil, err
	}
	if trustError != nil {
		err = validateUntrustedSelfSignedCertificate(certificate, time.Now())
		if err != nil {
			return nil, err
		}
	}
	return signer, nil
}

func verifiedSignerCertificate(stateData windows.Handle) ([]byte, error) {
	providerData := winTrustProviderDataFromStateData(stateData)
	if providerData == 0 {
		return nil, E.New("missing Authenticode provider data")
	}
	providerSigner := winTrustProviderSignerFromChain(providerData, 0, 0, 0)
	if providerSigner == 0 {
		return nil, E.New("missing Authenticode provider signer")
	}
	providerCertificate := winTrustProviderCertificateFromChain(providerSigner, 0)
	if providerCertificate == 0 {
		return nil, E.New("missing Authenticode provider certificate")
	}
	var providerCertificateValue cryptProviderCertificate
	err := readCurrentProcessValue(providerCertificate, &providerCertificateValue)
	if err != nil {
		return nil, E.Cause(err, "read Authenticode provider certificate")
	}
	if providerCertificateValue.certificateContext == nil {
		return nil, E.New("empty Authenticode signer certificate context")
	}
	var certificateContext windows.CertContext
	err = readCurrentProcessValue(uintptr(unsafe.Pointer(providerCertificateValue.certificateContext)), &certificateContext)
	if err != nil {
		return nil, E.Cause(err, "read Authenticode certificate context")
	}
	if certificateContext.EncodedCert == nil || certificateContext.Length == 0 {
		return nil, E.New("empty Authenticode signer certificate")
	}
	encodedCertificate, err := readCurrentProcessBytes(uintptr(unsafe.Pointer(certificateContext.EncodedCert)), certificateContext.Length)
	if err != nil {
		return nil, E.Cause(err, "read Authenticode signer certificate")
	}
	return encodedCertificate, nil
}

func validateCodeSigningCertificate(encodedCertificate []byte) (*x509.Certificate, error) {
	certificate, err := x509.ParseCertificate(encodedCertificate)
	if err != nil {
		return nil, E.Cause(err, "parse Authenticode signer certificate")
	}
	for _, usage := range certificate.ExtKeyUsage {
		if usage == x509.ExtKeyUsageCodeSigning || usage == x509.ExtKeyUsageAny {
			return certificate, nil
		}
	}
	return nil, E.New("Authenticode signer certificate is not valid for code signing")
}

func validateUntrustedSelfSignedCertificate(certificate *x509.Certificate, currentTime time.Time) error {
	if !bytes.Equal(certificate.RawSubject, certificate.RawIssuer) {
		return E.New("untrusted Authenticode signer certificate is not self-signed")
	}
	err := certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature)
	if err != nil {
		return E.Cause(err, "verify untrusted Authenticode signer self-signature")
	}
	if currentTime.Before(certificate.NotBefore) || currentTime.After(certificate.NotAfter) {
		return E.New("untrusted Authenticode signer certificate is not currently valid")
	}
	return nil
}
