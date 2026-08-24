package cachefile

import (
	"encoding/binary"
	"os"
	"time"

	"github.com/sagernet/bbolt"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/logger"
)

var bucketRDRC = []byte("rdrc2")

func (c *CacheFile) StoreRDRC() bool {
	return c.storeRDRC
}

func (c *CacheFile) RDRCTimeout() time.Duration {
	return c.rdrcTimeout
}

func (c *CacheFile) LoadRDRC(transportName string, qName string, qType uint16) (rejected bool) {
	c.persistenceAccess.Lock()
	_, cached := c.saveRDRC[saveCacheKey{transportName, qName, qType}]
	c.persistenceAccess.Unlock()
	if cached {
		return true
	}
	key := buf.Get(2 + len(qName))
	binary.BigEndian.PutUint16(key, qType)
	copy(key[2:], qName)
	defer buf.Put(key)
	var deleteCache bool
	err := c.view(func(tx *bbolt.Tx) error {
		bucket := c.bucket(tx, bucketRDRC)
		if bucket == nil {
			return nil
		}
		bucket = bucket.Bucket([]byte(transportName))
		if bucket == nil {
			return nil
		}
		content := bucket.Get(key)
		if content == nil {
			return nil
		}
		expiresAt := time.Unix(int64(binary.BigEndian.Uint64(content)), 0)
		if time.Now().After(expiresAt) {
			deleteCache = true
			return nil
		}
		rejected = true
		return nil
	})
	if err != nil {
		return
	}
	if deleteCache {
		c.update(func(tx *bbolt.Tx) error {
			bucket := c.bucket(tx, bucketRDRC)
			if bucket == nil {
				return nil
			}
			bucket = bucket.Bucket([]byte(transportName))
			if bucket == nil {
				return nil
			}
			return bucket.Delete(key)
		})
	}
	return
}

func (c *CacheFile) SaveRDRC(transportName string, qName string, qType uint16) error {
	return c.saveRDRCAt(transportName, qName, qType, time.Now().Add(c.rdrcTimeout))
}

func (c *CacheFile) saveRDRCAt(transportName string, qName string, qType uint16, expiration time.Time) error {
	expiresAt := buf.Get(8)
	defer buf.Put(expiresAt)
	binary.BigEndian.PutUint64(expiresAt, uint64(expiration.Unix()))
	return c.batch(func(tx *bbolt.Tx) error {
		bucket, err := c.createBucket(tx, bucketRDRC)
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
		return bucket.Put(key, expiresAt)
	})
}

func (c *CacheFile) SaveRDRCAsync(transportName string, qName string, qType uint16, targetLogger logger.Logger) {
	saveKey := saveCacheKey{transportName, qName, qType}
	c.persistenceAccess.Lock()
	if !c.persistenceStarted || c.persistenceClosing {
		shouldLog := c.rejectPersistenceLocked()
		c.persistenceAccess.Unlock()
		logPersistenceRejection(targetLogger, "RDRC", os.ErrClosed, shouldLog)
		return
	}
	entry, loaded := c.saveRDRC[saveKey]
	additionalEntries := 0
	additionalBytes := 0
	if !loaded {
		additionalEntries = 1
		additionalBytes = cacheKeyPersistenceSize(saveKey)
	}
	if !c.queueAvailableLocked(additionalEntries, additionalBytes) {
		shouldLog := c.rejectPersistenceLocked()
		c.persistenceAccess.Unlock()
		logPersistenceRejection(targetLogger, "RDRC", errPersistenceOverloaded, shouldLog)
		return
	}
	entry.expiresAt = time.Now().Add(c.rdrcTimeout)
	entry.sequence++
	entry.generation = c.rdrcGeneration
	c.saveRDRC[saveKey] = entry
	c.pendingEntries += additionalEntries
	c.pendingBytes += additionalBytes
	c.persistenceAccess.Unlock()
	c.notifyPersistenceWorker()
}
