package cachefile

import (
	"errors"
	"net/netip"
	"os"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/sagernet/sing/common/logger"
)

const (
	maxPendingPersistenceEntries = 1024
	maxPendingPersistenceBytes   = 8 << 20
)

var errPersistenceOverloaded = errors.New("cache persistence queue is full")

type persistenceKind uint8

const (
	persistenceDNS persistenceKind = iota
	persistenceRDRC
	persistenceFakeIP
	persistenceFakeIPMetadata
)

type persistenceItem struct {
	kind       persistenceKind
	key        saveCacheKey
	address    netip.Addr
	dnsEntry   saveDNSCacheEntry
	rdrcEntry  saveRDRCEntry
	fakeEntry  saveFakeIPEntry
	metadata   saveMetadataEntry
	generation uint64
	sequence   uint64
}

func (c *CacheFile) startPersistenceWorker() {
	c.persistenceAccess.Lock()
	if c.persistenceStarted || c.persistenceClosing {
		c.persistenceAccess.Unlock()
		return
	}
	c.persistenceStarted = true
	c.workerGroup.Add(1)
	c.persistenceAccess.Unlock()
	go func() {
		defer c.workerGroup.Done()
		c.loopPersistence()
	}()
}

func (c *CacheFile) notifyPersistenceWorker() {
	select {
	case c.persistenceWake <- struct{}{}:
	default:
	}
}

func (c *CacheFile) loopPersistence() {
	for {
		item, loaded, done := c.nextPersistenceItem()
		if done {
			return
		}
		if !loaded {
			<-c.persistenceWake
			continue
		}

		var saveErr error
		c.persistenceOperation.Lock()
		if c.persistenceItemCurrent(item) {
			if c.persistenceSaver != nil {
				saveErr = c.persistenceSaver(item)
			} else {
				switch item.kind {
				case persistenceDNS:
					saveErr = c.SaveDNSCache(item.key.TransportName, item.key.QuestionName, item.key.QType, item.dnsEntry.rawMessage, item.dnsEntry.expireAt)
				case persistenceRDRC:
					saveErr = c.saveRDRCAt(item.key.TransportName, item.key.QuestionName, item.key.QType, item.rdrcEntry.expiresAt)
				case persistenceFakeIP:
					saveErr = c.FakeIPStore(item.address, item.fakeEntry.domain)
				case persistenceFakeIPMetadata:
					saveErr = c.FakeIPSaveMetadata(item.metadata.metadata)
				}
			}
		}
		c.persistenceOperation.Unlock()

		c.finishPersistenceItem(item, saveErr)
	}
}

func (c *CacheFile) nextPersistenceItem() (persistenceItem, bool, bool) {
	c.persistenceAccess.Lock()
	defer c.persistenceAccess.Unlock()

	for offset := range 4 {
		kind := persistenceKind((int(c.persistenceCursor) + offset) % 4)
		switch kind {
		case persistenceDNS:
			for key, entry := range c.saveDNSCache {
				if entry.saving {
					continue
				}
				entry.saving = true
				c.saveDNSCache[key] = entry
				c.persistenceCursor = persistenceRDRC
				return persistenceItem{
					kind:       persistenceDNS,
					key:        key,
					dnsEntry:   entry,
					generation: entry.generation,
					sequence:   entry.sequence,
				}, true, false
			}
		case persistenceRDRC:
			for key, entry := range c.saveRDRC {
				if entry.saving {
					continue
				}
				entry.saving = true
				c.saveRDRC[key] = entry
				c.persistenceCursor = persistenceFakeIP
				return persistenceItem{
					kind:       persistenceRDRC,
					key:        key,
					rdrcEntry:  entry,
					generation: entry.generation,
					sequence:   entry.sequence,
				}, true, false
			}
		case persistenceFakeIP:
			for address, entry := range c.saveFakeIP {
				if entry.saving {
					continue
				}
				entry.saving = true
				c.saveFakeIP[address] = entry
				c.persistenceCursor = persistenceFakeIPMetadata
				return persistenceItem{
					kind:       persistenceFakeIP,
					address:    address,
					fakeEntry:  entry,
					generation: entry.generation,
					sequence:   entry.sequence,
				}, true, false
			}
		case persistenceFakeIPMetadata:
			if c.metadataSave.metadata != nil && !c.metadataSave.saving {
				c.metadataSave.saving = true
				entry := c.metadataSave
				c.persistenceCursor = persistenceDNS
				return persistenceItem{
					kind:       persistenceFakeIPMetadata,
					metadata:   entry,
					generation: entry.generation,
					sequence:   entry.sequence,
				}, true, false
			}
		}
	}
	if c.persistenceClosing && c.pendingEntries == 0 {
		return persistenceItem{}, false, true
	}
	return persistenceItem{}, false, false
}

func (c *CacheFile) persistenceItemCurrent(item persistenceItem) bool {
	c.persistenceAccess.Lock()
	defer c.persistenceAccess.Unlock()
	switch item.kind {
	case persistenceDNS:
		entry, loaded := c.saveDNSCache[item.key]
		return loaded && entry.sequence == item.sequence && entry.generation == item.generation && item.generation == c.dnsGeneration
	case persistenceRDRC:
		entry, loaded := c.saveRDRC[item.key]
		return loaded && entry.sequence == item.sequence && entry.generation == item.generation && item.generation == c.rdrcGeneration
	case persistenceFakeIP:
		entry, loaded := c.saveFakeIP[item.address]
		return loaded && entry.sequence == item.sequence && entry.generation == item.generation && item.generation == c.fakeIPGeneration
	case persistenceFakeIPMetadata:
		entry := c.metadataSave
		return entry.metadata != nil && entry.sequence == item.sequence && entry.generation == item.generation && item.generation == c.fakeIPGeneration
	default:
		return false
	}
}

func (c *CacheFile) finishPersistenceItem(item persistenceItem, saveErr error) {
	c.persistenceAccess.Lock()
	switch item.kind {
	case persistenceDNS:
		entry, loaded := c.saveDNSCache[item.key]
		if loaded && entry.sequence == item.sequence && entry.generation == item.generation {
			delete(c.saveDNSCache, item.key)
			c.pendingEntries--
			c.pendingBytes -= dnsPersistenceSize(item.key, entry)
		} else if loaded {
			entry.saving = false
			c.saveDNSCache[item.key] = entry
		}
	case persistenceRDRC:
		entry, loaded := c.saveRDRC[item.key]
		if loaded && entry.sequence == item.sequence && entry.generation == item.generation {
			delete(c.saveRDRC, item.key)
			c.pendingEntries--
			c.pendingBytes -= cacheKeyPersistenceSize(item.key)
		} else if loaded {
			entry.saving = false
			c.saveRDRC[item.key] = entry
		}
	case persistenceFakeIP:
		entry, loaded := c.saveFakeIP[item.address]
		if loaded && entry.sequence == item.sequence && entry.generation == item.generation {
			delete(c.saveFakeIP, item.address)
			if item.address.Is4() {
				if address, exists := c.saveAddress4[entry.domain]; exists && address == item.address {
					delete(c.saveAddress4, entry.domain)
				}
			} else if address, exists := c.saveAddress6[entry.domain]; exists && address == item.address {
				delete(c.saveAddress6, entry.domain)
			}
			c.pendingEntries--
			c.pendingBytes -= fakeIPPersistenceSize(entry)
		} else if loaded {
			entry.saving = false
			c.saveFakeIP[item.address] = entry
		}
	case persistenceFakeIPMetadata:
		entry := c.metadataSave
		if entry.metadata != nil && entry.sequence == item.sequence && entry.generation == item.generation {
			c.metadataSave = saveMetadataEntry{}
			c.pendingEntries--
			c.pendingBytes -= metadataPersistenceSize
		} else if entry.metadata != nil {
			entry.saving = false
			c.metadataSave = entry
		}
	}
	if c.pendingEntries < 0 {
		c.pendingEntries = 0
	}
	if c.pendingBytes < 0 {
		c.pendingBytes = 0
	}
	if saveErr != nil && !errors.Is(saveErr, os.ErrClosed) {
		c.persistenceErr = saveErr
	}
	c.persistenceAccess.Unlock()

	if saveErr != nil && c.logger != nil {
		c.logger.Warn("save cache persistence entry: ", saveErr)
	}
	c.notifyPersistenceWorker()
}

func (c *CacheFile) queueAvailableLocked(addEntries int, addBytes int) bool {
	return c.pendingEntries+addEntries <= maxPendingPersistenceEntries && c.pendingBytes+addBytes <= maxPendingPersistenceBytes
}

func (c *CacheFile) rejectPersistenceLocked() bool {
	c.overloadCount++
	now := time.Now()
	if c.overloadLoggedAt.IsZero() || now.Sub(c.overloadLoggedAt) >= time.Minute {
		c.overloadLoggedAt = now
		return true
	}
	return false
}

func logPersistenceRejection(target logger.Logger, kind string, reason error, shouldLog bool) {
	if shouldLog && target != nil {
		target.Warn("discard ", kind, " persistence update: ", reason)
	}
}

func cacheKeyPersistenceSize(key saveCacheKey) int {
	return len(key.TransportName) + len(key.QuestionName) + 32
}

func dnsPersistenceSize(key saveCacheKey, entry saveDNSCacheEntry) int {
	return cacheKeyPersistenceSize(key) + len(entry.rawMessage) + 32
}

func fakeIPPersistenceSize(entry saveFakeIPEntry) int {
	return len(entry.domain) + 64
}

const metadataPersistenceSize = 128

func cloneFakeIPMetadata(metadata *adapter.FakeIPMetadata) *adapter.FakeIPMetadata {
	if metadata == nil {
		return nil
	}
	cloned := *metadata
	return &cloned
}
