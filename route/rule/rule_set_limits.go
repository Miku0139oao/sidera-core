package rule

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"net/http"

	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
)

const (
	maxRemoteRuleSetWireSize         = 32 << 20
	maxRemoteRuleSetDecompressedSize = 64 << 20
	maxRuleSetRules                  = 1 << 18
	maxRuleSetCollectionEntries      = 1 << 22
	maxRuleSetTotalEntries           = 1 << 24
	maxRuleSetStringSize             = 64 << 10
	maxRuleSetNesting                = 32
)

func readRemoteRuleSetBody(response *http.Response) ([]byte, error) {
	if response.ContentLength > maxRemoteRuleSetWireSize {
		return nil, E.New("rule-set response exceeds wire size limit")
	}
	limitedReader := &io.LimitedReader{R: response.Body, N: maxRemoteRuleSetWireSize + 1}
	content, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}
	if len(content) > maxRemoteRuleSetWireSize {
		return nil, E.New("rule-set response exceeds wire size limit")
	}
	return content, nil
}

func validateRemoteRuleSetWireSize(content []byte) error {
	if len(content) > maxRemoteRuleSetWireSize {
		return E.New("rule-set exceeds wire size limit")
	}
	return nil
}

type ruleSetLimitBudget struct {
	rules   uint64
	entries uint64
	text    uint64
}

func (b *ruleSetLimitBudget) addRules(count uint64) error {
	if count > maxRuleSetRules || b.rules > maxRuleSetRules-count {
		return E.New("rule-set rule count exceeds limit")
	}
	b.rules += count
	return b.addEntries(count)
}

func (b *ruleSetLimitBudget) addEntries(count uint64) error {
	if count > maxRuleSetCollectionEntries || b.entries > maxRuleSetTotalEntries-count {
		return E.New("rule-set collection count exceeds limit")
	}
	b.entries += count
	return nil
}

func (b *ruleSetLimitBudget) addText(value string) error {
	if len(value) > maxRuleSetStringSize {
		return E.New("rule-set string exceeds size limit")
	}
	if uint64(len(value)) > maxRemoteRuleSetDecompressedSize-b.text {
		return E.New("rule-set text exceeds size limit")
	}
	b.text += uint64(len(value))
	return nil
}

func validatePlainRuleSetLimits(rules []option.HeadlessRule) error {
	budget := new(ruleSetLimitBudget)
	return validateHeadlessRuleLimits(rules, 0, budget)
}

func validateHeadlessRuleLimits(rules []option.HeadlessRule, depth int, budget *ruleSetLimitBudget) error {
	if depth > maxRuleSetNesting {
		return E.New("rule-set nesting exceeds limit")
	}
	if err := budget.addRules(uint64(len(rules))); err != nil {
		return err
	}
	for _, rawRule := range rules {
		switch rawRule.Type {
		case C.RuleTypeDefault, "":
			if err := validateDefaultHeadlessRuleLimits(rawRule.DefaultOptions, budget); err != nil {
				return err
			}
		case C.RuleTypeLogical:
			if err := validateHeadlessRuleLimits(rawRule.LogicalOptions.Rules, depth+1, budget); err != nil {
				return err
			}
		default:
			return E.New("unknown rule type: ", rawRule.Type)
		}
	}
	return nil
}

func validateDefaultHeadlessRuleLimits(rule option.DefaultHeadlessRule, budget *ruleSetLimitBudget) error {
	if err := addRuleSetValues(budget,
		len(rule.QueryType), len(rule.Network), len(rule.Domain), len(rule.DomainSuffix),
		len(rule.DomainKeyword), len(rule.DomainRegex), len(rule.SourceIPCIDR), len(rule.IPCIDR),
		len(rule.SourcePort), len(rule.SourcePortRange), len(rule.Port), len(rule.PortRange),
		len(rule.ProcessName), len(rule.ProcessPath), len(rule.ProcessPathRegex), len(rule.PackageName),
		len(rule.PackageNameRegex), len(rule.NetworkType), len(rule.WIFISSID), len(rule.WIFIBSSID),
		len(rule.DefaultInterfaceAddress), len(rule.AdGuardDomain),
	); err != nil {
		return err
	}
	for _, values := range [][]string{
		rule.Network, rule.Domain, rule.DomainSuffix, rule.DomainKeyword, rule.DomainRegex,
		rule.SourceIPCIDR, rule.IPCIDR, rule.SourcePortRange, rule.PortRange,
		rule.ProcessName, rule.ProcessPath, rule.ProcessPathRegex, rule.PackageName,
		rule.PackageNameRegex, rule.WIFISSID, rule.WIFIBSSID, rule.AdGuardDomain,
	} {
		for _, value := range values {
			if err := budget.addText(value); err != nil {
				return err
			}
		}
	}
	if rule.NetworkInterfaceAddress != nil {
		entries := rule.NetworkInterfaceAddress.Entries()
		if err := budget.addEntries(uint64(len(entries))); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := budget.addEntries(uint64(len(entry.Value))); err != nil {
				return err
			}
		}
	}
	return nil
}

func addRuleSetValues(budget *ruleSetLimitBudget, counts ...int) error {
	for _, count := range counts {
		if err := budget.addEntries(uint64(count)); err != nil {
			return err
		}
	}
	return nil
}

// validateBinaryRuleSetLimits walks the decompressed SRS stream without allocating
// count-sized objects. The normal decoder runs only after every count has been
// bounded, preventing tiny malformed inputs from causing large allocations.
func validateBinaryRuleSetLimits(content []byte) error {
	if len(content) < 4 || !bytes.Equal(content[:3], []byte{'S', 'R', 'S'}) {
		return E.New("invalid sing-box rule-set file")
	}
	if content[3] > C.RuleSetVersionCurrent {
		return E.New("unsupported rule-set version: ", content[3])
	}
	compressed, err := zlib.NewReader(bytes.NewReader(content[4:]))
	if err != nil {
		return err
	}
	defer compressed.Close()
	limited := &io.LimitedReader{R: compressed, N: maxRemoteRuleSetDecompressedSize + 1}
	reader := bufio.NewReader(limited)
	budget := new(ruleSetLimitBudget)
	ruleCount, err := binary.ReadUvarint(reader)
	if err != nil {
		return err
	}
	if err = validateBinaryRules(reader, ruleCount, 0, budget); err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, reader)
	if err != nil {
		return err
	}
	if limited.N <= 0 {
		return E.New("rule-set decompressed size exceeds limit")
	}
	return nil
}

func validateBinaryRules(reader *bufio.Reader, count uint64, depth int, budget *ruleSetLimitBudget) error {
	if depth > maxRuleSetNesting {
		return E.New("rule-set nesting exceeds limit")
	}
	if err := budget.addRules(count); err != nil {
		return err
	}
	for range count {
		ruleType, err := reader.ReadByte()
		if err != nil {
			return err
		}
		switch ruleType {
		case 0:
			if err = validateBinaryDefaultRule(reader, budget); err != nil {
				return err
			}
		case 1:
			if _, err = reader.ReadByte(); err != nil { // logical mode
				return err
			}
			nestedCount, readErr := binary.ReadUvarint(reader)
			if readErr != nil {
				return readErr
			}
			if err = validateBinaryRules(reader, nestedCount, depth+1, budget); err != nil {
				return err
			}
			if _, err = reader.ReadByte(); err != nil { // invert
				return err
			}
		default:
			return E.New("unknown rule type: ", ruleType)
		}
	}
	return nil
}

func validateBinaryDefaultRule(reader *bufio.Reader, budget *ruleSetLimitBudget) error {
	for {
		itemType, err := reader.ReadByte()
		if err != nil {
			return err
		}
		switch itemType {
		case 0, 7, 9: // uint16 slices
			if err = skipCountedFixed(reader, 2, budget); err != nil {
				return err
			}
		case 18: // uint8 slice
			if err = skipCountedFixed(reader, 1, budget); err != nil {
				return err
			}
		case 1, 3, 4, 8, 10, 11, 12, 13, 14, 15, 17, 23:
			if err = skipStringList(reader, budget); err != nil {
				return err
			}
		case 2, 16:
			if err = skipSuccinctSet(reader, budget); err != nil {
				return err
			}
		case 5, 6:
			if err = skipIPSet(reader, budget); err != nil {
				return err
			}
		case 19, 20: // booleans encoded by presence
		case 21:
			if err = skipInterfaceAddressMap(reader, budget); err != nil {
				return err
			}
		case 22:
			if err = skipPrefixList(reader, budget); err != nil {
				return err
			}
		case 0xff:
			_, err = reader.ReadByte() // invert
			return err
		default:
			return E.New("unknown rule item type: ", itemType)
		}
	}
}

func skipCountedFixed(reader *bufio.Reader, width uint64, budget *ruleSetLimitBudget) error {
	count, err := binary.ReadUvarint(reader)
	if err != nil {
		return err
	}
	if err = budget.addEntries(count); err != nil {
		return err
	}
	if count > maxRemoteRuleSetDecompressedSize/width {
		return E.New("rule-set collection size exceeds limit")
	}
	return discardRuleSetBytes(reader, count*width)
}

func skipStringList(reader *bufio.Reader, budget *ruleSetLimitBudget) error {
	count, err := binary.ReadUvarint(reader)
	if err != nil {
		return err
	}
	if err = budget.addEntries(count); err != nil {
		return err
	}
	for range count {
		length, readErr := binary.ReadUvarint(reader)
		if readErr != nil {
			return readErr
		}
		if length > maxRuleSetStringSize {
			return E.New("rule-set string exceeds size limit")
		}
		if budget.text > maxRemoteRuleSetDecompressedSize-length {
			return E.New("rule-set text exceeds size limit")
		}
		budget.text += length
		if readErr = discardRuleSetBytes(reader, length); readErr != nil {
			return readErr
		}
	}
	return nil
}

func skipSuccinctSet(reader *bufio.Reader, budget *ruleSetLimitBudget) error {
	version, err := reader.ReadByte()
	if err != nil {
		return err
	}
	if version != 0 {
		return E.New("unsupported domain matcher version: ", version)
	}
	if err = skipCountedFixed(reader, 8, budget); err != nil {
		return err
	}
	if err = skipCountedFixed(reader, 8, budget); err != nil {
		return err
	}
	return skipCountedFixed(reader, 1, budget)
}

func skipIPSet(reader *bufio.Reader, budget *ruleSetLimitBudget) error {
	version, err := reader.ReadByte()
	if err != nil {
		return err
	}
	if version != 1 {
		return E.New("unsupported IP set version: ", version)
	}
	var count uint64
	if err = binary.Read(reader, binary.BigEndian, &count); err != nil {
		return err
	}
	if err = budget.addEntries(count); err != nil {
		return err
	}
	for range count {
		if err = skipIPAddress(reader); err != nil {
			return err
		}
		if err = skipIPAddress(reader); err != nil {
			return err
		}
	}
	return nil
}

func skipIPAddress(reader *bufio.Reader) error {
	length, err := binary.ReadUvarint(reader)
	if err != nil {
		return err
	}
	if length != 4 && length != 16 {
		return E.New("invalid IP address length: ", length)
	}
	return discardRuleSetBytes(reader, length)
}

func skipPrefix(reader *bufio.Reader) error {
	if err := skipIPAddress(reader); err != nil {
		return err
	}
	_, err := reader.ReadByte()
	return err
}

func skipPrefixList(reader *bufio.Reader, budget *ruleSetLimitBudget) error {
	count, err := binary.ReadUvarint(reader)
	if err != nil {
		return err
	}
	if err = budget.addEntries(count); err != nil {
		return err
	}
	for range count {
		if err = skipPrefix(reader); err != nil {
			return err
		}
	}
	return nil
}

func skipInterfaceAddressMap(reader *bufio.Reader, budget *ruleSetLimitBudget) error {
	count, err := binary.ReadUvarint(reader)
	if err != nil {
		return err
	}
	if err = budget.addEntries(count); err != nil {
		return err
	}
	for range count {
		if _, err = reader.ReadByte(); err != nil {
			return err
		}
		if err = skipPrefixList(reader, budget); err != nil {
			return err
		}
	}
	return nil
}

func discardRuleSetBytes(reader io.Reader, count uint64) error {
	if count > maxRemoteRuleSetDecompressedSize {
		return E.New("rule-set field size exceeds limit")
	}
	_, err := io.CopyN(io.Discard, reader, int64(count))
	return err
}
