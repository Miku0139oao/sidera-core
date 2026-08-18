// SPDX-License-Identifier: MPL-2.0
//
// Adapted from Xray-core infra/conf/vless.go at commit
// 6e3322d219140a025285ded1114fe17a5edb74d8. Portions copyright the
// Xray-core contributors and are licensed under the Mozilla Public License 2.0.

package xrayencryption

import (
	"crypto/ecdh"
	"crypto/mlkem"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const methodName = "mlkem768x25519plus"

func ParseDecryption(config string) (*ServerInstance, error) {
	if config == "none" {
		return nil, nil
	}
	if config == "" {
		return nil, errors.New("xray encryption: empty decryption")
	}
	fields := strings.Split(config, ".")
	if len(fields) < 4 || fields[0] != methodName {
		return nil, fmt.Errorf("xray encryption: unsupported decryption %q", config)
	}
	xorMode, err := parseMode(fields[1])
	if err != nil {
		return nil, err
	}
	secondsFrom, secondsTo, err := parseSecondsRange(fields[2])
	if err != nil {
		return nil, err
	}
	padding, keyFields, err := splitPaddingAndKeys(fields[3:])
	if err != nil {
		return nil, err
	}
	keys, err := decodeKeys(keyFields, 32, 64)
	if err != nil {
		return nil, err
	}
	instance := new(ServerInstance)
	if err := instance.Init(keys, xorMode, secondsFrom, secondsTo, padding); err != nil {
		return nil, fmt.Errorf("xray encryption: initialize server: %w", err)
	}
	return instance, nil
}

func NewServer(config string) (*ServerInstance, error) { return ParseDecryption(config) }

func ParseEncryption(config string) (*ClientInstance, error) {
	if config == "none" {
		return nil, nil
	}
	if config == "" {
		return nil, errors.New("xray encryption: empty encryption")
	}
	fields := strings.Split(config, ".")
	if len(fields) < 4 || fields[0] != methodName {
		return nil, fmt.Errorf("xray encryption: unsupported encryption %q", config)
	}
	xorMode, err := parseMode(fields[1])
	if err != nil {
		return nil, err
	}
	var seconds uint32
	switch fields[2] {
	case "1rtt":
	case "0rtt":
		seconds = 1
	default:
		return nil, fmt.Errorf("xray encryption: unsupported handshake mode %q", fields[2])
	}
	padding, keyFields, err := splitPaddingAndKeys(fields[3:])
	if err != nil {
		return nil, err
	}
	keys, err := decodeKeys(keyFields, 32, 1184)
	if err != nil {
		return nil, err
	}
	instance := new(ClientInstance)
	if err := instance.Init(keys, xorMode, seconds, padding); err != nil {
		return nil, fmt.Errorf("xray encryption: initialize client: %w", err)
	}
	return instance, nil
}

func NewClient(config string) (*ClientInstance, error) { return ParseEncryption(config) }

func ClientEncryptionFromDecryption(config string) (string, error) {
	if config == "none" {
		return "none", nil
	}
	if config == "" {
		return "", errors.New("xray encryption: empty decryption")
	}
	fields := strings.Split(config, ".")
	if len(fields) < 4 || fields[0] != methodName {
		return "", fmt.Errorf("xray encryption: unsupported decryption %q", config)
	}
	if _, err := parseMode(fields[1]); err != nil {
		return "", err
	}
	secondsFrom, secondsTo, err := parseSecondsRange(fields[2])
	if err != nil {
		return "", err
	}
	padding, keyFields, err := splitPaddingAndKeys(fields[3:])
	if err != nil {
		return "", err
	}
	keys, err := decodeKeys(keyFields, 32, 64)
	if err != nil {
		return "", err
	}
	var paddingLens, paddingGaps [][3]int
	if err = ParsePadding(padding, &paddingLens, &paddingGaps); err != nil {
		return "", err
	}

	clientKeyFields := make([]string, len(keys))
	for index, key := range keys {
		var publicKey []byte
		switch len(key) {
		case 32:
			privateKey, keyErr := ecdh.X25519().NewPrivateKey(key)
			if keyErr != nil {
				return "", keyErr
			}
			publicKey = privateKey.PublicKey().Bytes()
		case 64:
			privateKey, keyErr := mlkem.NewDecapsulationKey768(key)
			if keyErr != nil {
				return "", keyErr
			}
			publicKey = privateKey.EncapsulationKey().Bytes()
		default:
			return "", fmt.Errorf("xray encryption: invalid key %d length %d", index, len(key))
		}
		clientKeyFields[index] = base64.RawURLEncoding.EncodeToString(publicKey)
	}

	handshakeMode := "1rtt"
	if secondsFrom > 0 || secondsTo > 0 {
		handshakeMode = "0rtt"
	}
	clientFields := []string{methodName, fields[1], handshakeMode}
	if padding != "" {
		clientFields = append(clientFields, strings.Split(padding, ".")...)
	}
	clientFields = append(clientFields, clientKeyFields...)
	return strings.Join(clientFields, "."), nil
}

func parseMode(mode string) (uint32, error) {
	switch mode {
	case "native":
		return 0, nil
	case "xorpub":
		return 1, nil
	case "random":
		return 2, nil
	default:
		return 0, fmt.Errorf("xray encryption: unsupported mode %q", mode)
	}
}

func parseSecondsRange(value string) (int64, int64, error) {
	trimmed := strings.TrimSuffix(value, "s")
	parts := strings.SplitN(trimmed, "-", 2)
	from, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || from < 0 {
		return 0, 0, fmt.Errorf("xray encryption: invalid seconds range %q", value)
	}
	var to int64
	if len(parts) == 2 {
		to, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || to < 0 {
			return 0, 0, fmt.Errorf("xray encryption: invalid seconds range %q", value)
		}
	}
	return from, to, nil
}

func splitPaddingAndKeys(fields []string) (string, []string, error) {
	keyIndex := 0
	for keyIndex < len(fields) && len(fields[keyIndex]) < 20 {
		keyIndex++
	}
	if keyIndex == len(fields) {
		return "", nil, errors.New("xray encryption: missing key")
	}
	for _, field := range fields[keyIndex:] {
		if len(field) < 20 {
			return "", nil, errors.New("xray encryption: padding fields must precede keys")
		}
	}
	return strings.Join(fields[:keyIndex], "."), fields[keyIndex:], nil
}

func decodeKeys(fields []string, validLengths ...int) ([][]byte, error) {
	keys := make([][]byte, len(fields))
	for index, field := range fields {
		key, err := base64.RawURLEncoding.DecodeString(field)
		if err != nil {
			return nil, fmt.Errorf("xray encryption: invalid key %d: %w", index, err)
		}
		valid := false
		for _, length := range validLengths {
			valid = valid || len(key) == length
		}
		if !valid {
			return nil, fmt.Errorf("xray encryption: invalid key %d length %d", index, len(key))
		}
		keys[index] = key
	}
	return keys, nil
}
