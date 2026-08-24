package cachefile

import (
	"encoding/binary"
	"os"
	"time"

	"github.com/sagernet/bbolt"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
)

var bucketDNSCache = []byte("dns_cache")

func (c *CacheFile) StoreDNS() bool {
	return c.storeDNS
}

func (c *CacheFile) LoadDNSCache(transportName string, qName string, qType uint16) (rawMessage []byte, expireAt time.Time, loaded bool) {
	c.persistenceAccess.Lock()
	entry, cached := c.saveDNSCache[saveCacheKey{transportName, qName, qType}]
	c.persistenceAccess.Unlock()
	if cached {
		return entry.rawMessage, entry.expireAt, true
	}
	key := buf.Get(2 + len(qName))
	binary.BigEndian.PutUint16(key, qType)
	copy(key[2:], qName)
	defer buf.Put(key)
	err := c.view(func(tx *bbolt.Tx) error {
		bucket := c.bucket(tx, bucketDNSCache)
		if bucket == nil {
			return nil
		}
		bucket = bucket.Bucket([]byte(transportName))
		if bucket == nil {
			return nil
		}
		content := bucket.Get(key)
		if len(content) < 8 {
			return nil
		}
		expireAt = time.Unix(int64(binary.BigEndian.Uint64(content[:8])), 0)
		rawMessage = make([]byte, len(content)-8)
		copy(rawMessage, content[8:])
		loaded = true
		return nil
	})
	if err != nil {
		return nil, time.Time{}, false
	}
	return
}

func (c *CacheFile) SaveDNSCache(transportName string, qName string, qType uint16, rawMessage []byte, expireAt time.Time) error {
	value := buf.Get(8 + len(rawMessage))
	defer buf.Put(value)
	binary.BigEndian.PutUint64(value[:8], uint64(expireAt.Unix()))
	copy(value[8:], rawMessage)
	return c.batch(func(tx *bbolt.Tx) error {
		bucket, err := c.createBucket(tx, bucketDNSCache)
		if err != nil {
			return err
		}
		bucket, err = bucket.CreateBucketIfNotExists([]byte(transportName))
		if err != nil {
			return err
		}
		key := buf.Get(2 + len(qName))
		binary.BigEndian.PutUint16(key, qType)
		copy(key[2:], qName)
		defer buf.Put(key)
		return bucket.Put(key, value)
	})
}

func (c *CacheFile) SaveDNSCacheAsync(transportName string, qName string, qType uint16, rawMessage []byte, expireAt time.Time, targetLogger logger.Logger) {
	saveKey := saveCacheKey{transportName, qName, qType}
	c.persistenceAccess.Lock()
	if !c.persistenceStarted || c.persistenceClosing {
		shouldLog := c.rejectPersistenceLocked()
		c.persistenceAccess.Unlock()
		logPersistenceRejection(targetLogger, "DNS cache", os.ErrClosed, shouldLog)
		return
	}
	entry, loaded := c.saveDNSCache[saveKey]
	additionalEntries := 0
	additionalBytes := len(rawMessage) - len(entry.rawMessage)
	if !loaded {
		additionalEntries = 1
		additionalBytes = cacheKeyPersistenceSize(saveKey) + len(rawMessage) + 32
	}
	if !c.queueAvailableLocked(additionalEntries, additionalBytes) {
		shouldLog := c.rejectPersistenceLocked()
		c.persistenceAccess.Unlock()
		logPersistenceRejection(targetLogger, "DNS cache", errPersistenceOverloaded, shouldLog)
		return
	}
	entry.rawMessage = append([]byte(nil), rawMessage...)
	entry.expireAt = expireAt
	entry.sequence++
	entry.generation = c.dnsGeneration
	c.saveDNSCache[saveKey] = entry
	c.pendingEntries += additionalEntries
	c.pendingBytes += additionalBytes
	c.persistenceAccess.Unlock()
	c.notifyPersistenceWorker()
}

func (c *CacheFile) ClearDNSCache() error {
	c.persistenceOperation.Lock()
	defer c.persistenceOperation.Unlock()
	c.persistenceAccess.Lock()
	c.dnsGeneration++
	for key, entry := range c.saveDNSCache {
		c.pendingEntries--
		c.pendingBytes -= dnsPersistenceSize(key, entry)
	}
	clear(c.saveDNSCache)
	c.persistenceAccess.Unlock()
	return c.batch(func(tx *bbolt.Tx) error {
		if c.cacheID == nil {
			bucket := tx.Bucket(bucketDNSCache)
			if bucket == nil {
				return nil
			}
			return tx.DeleteBucket(bucketDNSCache)
		}
		bucket := tx.Bucket(c.cacheID)
		if bucket == nil || bucket.Bucket(bucketDNSCache) == nil {
			return nil
		}
		return bucket.DeleteBucket(bucketDNSCache)
	})
}

func (c *CacheFile) loopCacheCleanup(interval time.Duration, cleanupFunc func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.cleanupStop:
			return
		case <-ticker.C:
			cleanupFunc()
		}
	}
}

func (c *CacheFile) cleanupDNSCache() {
	now := time.Now()
	err := c.batch(func(tx *bbolt.Tx) error {
		bucket := c.bucket(tx, bucketDNSCache)
		if bucket == nil {
			return nil
		}
		var emptyTransports [][]byte
		err := bucket.ForEachBucket(func(transportName []byte) error {
			transportBucket := bucket.Bucket(transportName)
			if transportBucket == nil {
				return nil
			}
			var expiredKeys [][]byte
			err := transportBucket.ForEach(func(key, value []byte) error {
				if len(value) < 8 {
					expiredKeys = append(expiredKeys, append([]byte(nil), key...))
					return nil
				}
				if c.disableExpire {
					return nil
				}
				expireAt := time.Unix(int64(binary.BigEndian.Uint64(value[:8])), 0)
				if now.After(expireAt.Add(c.optimisticTimeout)) {
					expiredKeys = append(expiredKeys, append([]byte(nil), key...))
				}
				return nil
			})
			if err != nil {
				return err
			}
			for _, key := range expiredKeys {
				err = transportBucket.Delete(key)
				if err != nil {
					return err
				}
			}
			first, _ := transportBucket.Cursor().First()
			if first == nil {
				emptyTransports = append(emptyTransports, append([]byte(nil), transportName...))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, name := range emptyTransports {
			err = bucket.DeleteBucket(name)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.logger.Warn("cleanup DNS cache: ", err)
	}
}

func (c *CacheFile) clearRDRC() {
	c.persistenceOperation.Lock()
	defer c.persistenceOperation.Unlock()
	c.persistenceAccess.Lock()
	c.rdrcGeneration++
	for key := range c.saveRDRC {
		c.pendingEntries--
		c.pendingBytes -= cacheKeyPersistenceSize(key)
	}
	clear(c.saveRDRC)
	c.persistenceAccess.Unlock()
	err := c.batch(func(tx *bbolt.Tx) error {
		if c.cacheID == nil {
			if tx.Bucket(bucketRDRC) == nil {
				return nil
			}
			return tx.DeleteBucket(bucketRDRC)
		}
		bucket := tx.Bucket(c.cacheID)
		if bucket == nil || bucket.Bucket(bucketRDRC) == nil {
			return nil
		}
		return bucket.DeleteBucket(bucketRDRC)
	})
	if err != nil {
		c.logger.Warn("clear RDRC: ", err)
	}
}

func (c *CacheFile) cleanupRDRC() {
	now := time.Now()
	err := c.batch(func(tx *bbolt.Tx) error {
		bucket := c.bucket(tx, bucketRDRC)
		if bucket == nil {
			return nil
		}
		var emptyTransports [][]byte
		err := bucket.ForEachBucket(func(transportName []byte) error {
			transportBucket := bucket.Bucket(transportName)
			if transportBucket == nil {
				return nil
			}
			var expiredKeys [][]byte
			err := transportBucket.ForEach(func(key, value []byte) error {
				if len(value) < 8 {
					expiredKeys = append(expiredKeys, append([]byte(nil), key...))
					return nil
				}
				expiresAt := time.Unix(int64(binary.BigEndian.Uint64(value)), 0)
				if now.After(expiresAt) {
					expiredKeys = append(expiredKeys, append([]byte(nil), key...))
				}
				return nil
			})
			if err != nil {
				return err
			}
			for _, key := range expiredKeys {
				err = transportBucket.Delete(key)
				if err != nil {
					return err
				}
			}
			first, _ := transportBucket.Cursor().First()
			if first == nil {
				emptyTransports = append(emptyTransports, append([]byte(nil), transportName...))
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, name := range emptyTransports {
			err = bucket.DeleteBucket(name)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		c.logger.Warn("cleanup RDRC: ", err)
	}
}
