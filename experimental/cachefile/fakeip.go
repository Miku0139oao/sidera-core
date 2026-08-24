package cachefile

import (
	"net/netip"
	"os"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/sagernet/bbolt"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

const fakeipBucketPrefix = "fakeip_"

var (
	bucketFakeIP        = []byte(fakeipBucketPrefix + "address")
	bucketFakeIPDomain4 = []byte(fakeipBucketPrefix + "domain4")
	bucketFakeIPDomain6 = []byte(fakeipBucketPrefix + "domain6")
	keyMetadata         = []byte(fakeipBucketPrefix + "metadata")
)

func (c *CacheFile) FakeIPMetadata() *adapter.FakeIPMetadata {
	var metadata adapter.FakeIPMetadata
	err := c.batch(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bucketFakeIP)
		if bucket == nil {
			return os.ErrNotExist
		}
		metadataBinary := bucket.Get(keyMetadata)
		if len(metadataBinary) == 0 {
			return os.ErrInvalid
		}
		err := bucket.Delete(keyMetadata)
		if err != nil {
			return err
		}
		return metadata.UnmarshalBinary(metadataBinary)
	})
	if err != nil {
		return nil
	}
	return &metadata
}

func (c *CacheFile) FakeIPSaveMetadata(metadata *adapter.FakeIPMetadata) error {
	return c.batch(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(bucketFakeIP)
		if err != nil {
			return err
		}
		metadataBinary, err := metadata.MarshalBinary()
		if err != nil {
			return err
		}
		return bucket.Put(keyMetadata, metadataBinary)
	})
}

func (c *CacheFile) FakeIPSaveMetadataAsync(metadata *adapter.FakeIPMetadata) {
	if metadata == nil {
		return
	}
	c.persistenceAccess.Lock()
	if !c.persistenceStarted || c.persistenceClosing {
		shouldLog := c.rejectPersistenceLocked()
		c.persistenceAccess.Unlock()
		logPersistenceRejection(c.logger, "FakeIP metadata", os.ErrClosed, shouldLog)
		return
	}
	entry := c.metadataSave
	additionalEntries := 0
	additionalBytes := 0
	if entry.metadata == nil {
		additionalEntries = 1
		additionalBytes = metadataPersistenceSize
	}
	if !c.queueAvailableLocked(additionalEntries, additionalBytes) {
		shouldLog := c.rejectPersistenceLocked()
		c.persistenceAccess.Unlock()
		logPersistenceRejection(c.logger, "FakeIP metadata", errPersistenceOverloaded, shouldLog)
		return
	}
	entry.metadata = cloneFakeIPMetadata(metadata)
	entry.sequence++
	entry.generation = c.fakeIPGeneration
	c.metadataSave = entry
	c.pendingEntries += additionalEntries
	c.pendingBytes += additionalBytes
	c.persistenceAccess.Unlock()
	c.notifyPersistenceWorker()
}

func (c *CacheFile) FakeIPStore(address netip.Addr, domain string) error {
	return c.batch(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(bucketFakeIP)
		if err != nil {
			return err
		}
		oldDomain := bucket.Get(address.AsSlice())
		err = bucket.Put(address.AsSlice(), []byte(domain))
		if err != nil {
			return err
		}
		if address.Is4() {
			bucket, err = tx.CreateBucketIfNotExists(bucketFakeIPDomain4)
		} else {
			bucket, err = tx.CreateBucketIfNotExists(bucketFakeIPDomain6)
		}
		if err != nil {
			return err
		}
		if oldDomain != nil {
			if err := bucket.Delete(oldDomain); err != nil {
				return err
			}
		}
		return bucket.Put([]byte(domain), address.AsSlice())
	})
}

func (c *CacheFile) FakeIPStoreAsync(address netip.Addr, domain string, targetLogger logger.Logger) {
	c.persistenceAccess.Lock()
	if !c.persistenceStarted || c.persistenceClosing {
		shouldLog := c.rejectPersistenceLocked()
		c.persistenceAccess.Unlock()
		logPersistenceRejection(targetLogger, "FakeIP", os.ErrClosed, shouldLog)
		return
	}
	entry, loaded := c.saveFakeIP[address]
	additionalEntries := 0
	additionalBytes := len(domain) - len(entry.domain)
	if !loaded {
		additionalEntries = 1
		additionalBytes = len(domain) + 64
	}
	if !c.queueAvailableLocked(additionalEntries, additionalBytes) {
		shouldLog := c.rejectPersistenceLocked()
		c.persistenceAccess.Unlock()
		logPersistenceRejection(targetLogger, "FakeIP", errPersistenceOverloaded, shouldLog)
		return
	}
	if loaded && entry.domain != domain {
		if address.Is4() {
			if savedAddress, exists := c.saveAddress4[entry.domain]; exists && savedAddress == address {
				delete(c.saveAddress4, entry.domain)
			}
		} else if savedAddress, exists := c.saveAddress6[entry.domain]; exists && savedAddress == address {
			delete(c.saveAddress6, entry.domain)
		}
	}
	entry.domain = domain
	entry.sequence++
	entry.generation = c.fakeIPGeneration
	c.saveFakeIP[address] = entry
	if address.Is4() {
		c.saveAddress4[domain] = address
	} else {
		c.saveAddress6[domain] = address
	}
	c.pendingEntries += additionalEntries
	c.pendingBytes += additionalBytes
	c.persistenceAccess.Unlock()
	c.notifyPersistenceWorker()
}

func (c *CacheFile) FakeIPLoad(address netip.Addr) (string, bool) {
	c.persistenceAccess.Lock()
	entry, cached := c.saveFakeIP[address]
	c.persistenceAccess.Unlock()
	if cached {
		return entry.domain, true
	}
	var domain string
	_ = c.view(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bucketFakeIP)
		if bucket == nil {
			return nil
		}
		domain = string(bucket.Get(address.AsSlice()))
		return nil
	})
	return domain, domain != ""
}

func (c *CacheFile) FakeIPLoadDomain(domain string, isIPv6 bool) (netip.Addr, bool) {
	var (
		cachedAddress netip.Addr
		cached        bool
	)
	c.persistenceAccess.Lock()
	if !isIPv6 {
		cachedAddress, cached = c.saveAddress4[domain]
	} else {
		cachedAddress, cached = c.saveAddress6[domain]
	}
	c.persistenceAccess.Unlock()
	if cached {
		return cachedAddress, true
	}
	var address netip.Addr
	_ = c.view(func(tx *bbolt.Tx) error {
		var bucket *bbolt.Bucket
		if isIPv6 {
			bucket = tx.Bucket(bucketFakeIPDomain6)
		} else {
			bucket = tx.Bucket(bucketFakeIPDomain4)
		}
		if bucket == nil {
			return nil
		}
		address = M.AddrFromIP(bucket.Get([]byte(domain)))
		return nil
	})
	return address, address.IsValid()
}

func (c *CacheFile) FakeIPReset() error {
	c.persistenceOperation.Lock()
	defer c.persistenceOperation.Unlock()
	c.persistenceAccess.Lock()
	c.fakeIPGeneration++
	for _, entry := range c.saveFakeIP {
		c.pendingEntries--
		c.pendingBytes -= fakeIPPersistenceSize(entry)
	}
	clear(c.saveFakeIP)
	clear(c.saveAddress4)
	clear(c.saveAddress6)
	if c.metadataSave.metadata != nil {
		c.pendingEntries--
		c.pendingBytes -= metadataPersistenceSize
		c.metadataSave = saveMetadataEntry{}
	}
	c.persistenceAccess.Unlock()
	return c.batch(func(tx *bbolt.Tx) error {
		err := tx.DeleteBucket(bucketFakeIP)
		if err != nil {
			return err
		}
		err = tx.DeleteBucket(bucketFakeIPDomain4)
		if err != nil {
			return err
		}
		return tx.DeleteBucket(bucketFakeIPDomain6)
	})
}
