package tf

import "encoding/binary"

const (
	recordLayerHeaderLen    int    = 5
	handshakeHeaderLen      int    = 6
	randomDataLen           int    = 32
	sessionIDHeaderLen      int    = 1
	cipherSuiteHeaderLen    int    = 2
	compressMethodHeaderLen int    = 1
	extensionsHeaderLen     int    = 2
	extensionHeaderLen      int    = 4
	sniExtensionHeaderLen   int    = 5
	contentType             uint8  = 22
	handshakeType           uint8  = 1
	sniExtensionType        uint16 = 0
	sniNameDNSHostnameType  uint8  = 0
	tlsVersionBitmask       uint16 = 0xFFFC
	tls13                   uint16 = 0x0304
)

type MyServerName struct {
	Index                     int
	Length                    int
	ServerName                string
	ExtensionsListLengthIndex int
}

func IndexTLSServerName(payload []byte) *MyServerName {
	if len(payload) < recordLayerHeaderLen || payload[0] != contentType {
		return nil
	}
	segmentLen := int(binary.BigEndian.Uint16(payload[3:5]))
	if len(payload) < recordLayerHeaderLen+segmentLen {
		return nil
	}
	serverName := indexTLSServerNameFromHandshake(payload[recordLayerHeaderLen : recordLayerHeaderLen+segmentLen])
	if serverName == nil {
		return nil
	}
	serverName.Index += recordLayerHeaderLen
	serverName.ExtensionsListLengthIndex += recordLayerHeaderLen
	if serverName.Length == 0 || serverName.Index < 0 || serverName.Index+serverName.Length > len(payload) {
		return nil
	}
	return serverName
}

func indexTLSServerNameFromHandshake(handshake []byte) *MyServerName {
	if len(handshake) < handshakeHeaderLen+randomDataLen+sessionIDHeaderLen {
		return nil
	}
	if handshake[0] != handshakeType {
		return nil
	}
	handshakeLen := int(handshake[1])<<16 | int(handshake[2])<<8 | int(handshake[3])
	if len(handshake[4:]) != handshakeLen {
		return nil
	}
	tlsVersion := binary.BigEndian.Uint16(handshake[4:6])
	if tlsVersion&tlsVersionBitmask != 0x0300 && tlsVersion != tls13 {
		return nil
	}
	sessionIDLen := int(handshake[38])
	currentIndex := handshakeHeaderLen + randomDataLen + sessionIDHeaderLen + sessionIDLen
	if len(handshake) < currentIndex+cipherSuiteHeaderLen {
		return nil
	}

	cipherSuites := handshake[currentIndex:]
	csLen := int(binary.BigEndian.Uint16(cipherSuites))
	if csLen < 2 || csLen%2 != 0 || len(cipherSuites) < cipherSuiteHeaderLen+csLen+compressMethodHeaderLen {
		return nil
	}
	compressMethodLen := int(cipherSuites[cipherSuiteHeaderLen+csLen])
	if compressMethodLen < 1 {
		return nil
	}
	currentIndex += cipherSuiteHeaderLen + csLen + compressMethodHeaderLen + compressMethodLen
	if len(handshake) < currentIndex {
		return nil
	}

	serverName := indexTLSServerNameFromExtensions(handshake[currentIndex:])
	if serverName == nil {
		return nil
	}
	serverName.Index += currentIndex
	serverName.ExtensionsListLengthIndex = currentIndex
	return serverName
}

func indexTLSServerNameFromExtensions(extensions []byte) *MyServerName {
	if len(extensions) < extensionsHeaderLen {
		return nil
	}
	extensionsLen := int(binary.BigEndian.Uint16(extensions))
	if len(extensions)-extensionsHeaderLen != extensionsLen {
		return nil
	}
	extensions = extensions[extensionsHeaderLen:]

	var indexedServerName *MyServerName
	for currentIndex := extensionsHeaderLen; len(extensions) > 0; {
		if len(extensions) < extensionHeaderLen {
			return nil
		}
		extensionType := binary.BigEndian.Uint16(extensions)
		extensionLen := int(binary.BigEndian.Uint16(extensions[2:4]))
		if len(extensions) < extensionHeaderLen+extensionLen {
			return nil
		}
		extension := extensions[extensionHeaderLen : extensionHeaderLen+extensionLen]

		if extensionType == sniExtensionType {
			if len(extension) < 2 {
				return nil
			}
			serverNameListLen := int(binary.BigEndian.Uint16(extension))
			if serverNameListLen != len(extension)-2 {
				return nil
			}
			serverNameList := extension[2:]
			entryOffset := 0
			for len(serverNameList) > 0 {
				if len(serverNameList) < 3 {
					return nil
				}
				nameType := serverNameList[0]
				nameLen := int(binary.BigEndian.Uint16(serverNameList[1:3]))
				if len(serverNameList) < 3+nameLen {
					return nil
				}
				if nameType == sniNameDNSHostnameType {
					if nameLen == 0 || indexedServerName != nil {
						return nil
					}
					name := serverNameList[3 : 3+nameLen]
					indexedServerName = &MyServerName{
						Index:      currentIndex + extensionHeaderLen + 2 + entryOffset + 3,
						Length:     nameLen,
						ServerName: string(name),
					}
				}
				serverNameList = serverNameList[3+nameLen:]
				entryOffset += 3 + nameLen
			}
		}
		extensions = extensions[extensionHeaderLen+extensionLen:]
		currentIndex += extensionHeaderLen + extensionLen
	}
	return indexedServerName
}
