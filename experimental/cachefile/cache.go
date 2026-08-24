package cachefile

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/Miku0139oao/sidera-core/experimental/deprecated"
	"github.com/Miku0139oao/sidera-core/option"
	"github.com/sagernet/bbolt"
	bboltErrors "github.com/sagernet/bbolt/errors"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/service/filemanager"
)

var (
	bucketSelected = []byte("selected")
	bucketExpand   = []byte("group_expand")
	bucketMode     = []byte("clash_mode")
	bucketRuleSet  = []byte("rule_set")

	bucketNameList = []string{
		string(bucketSelected),
		string(bucketExpand),
		string(bucketMode),
		string(bucketRuleSet),
		string(bucketRDRC),
		string(bucketDNSCache),
	}

	cacheIDDefault = []byte("default")
)

var _ adapter.CacheFile = (*CacheFile)(nil)

type CacheFile struct {
	ctx               context.Context
	logger            logger.Logger
	path              string
	cacheID           []byte
	cacheIDText       string
	storeFakeIP       bool
	storeRDRC         bool
	storeDNS          bool
	disableExpire     bool
	rdrcTimeout       time.Duration
	optimisticTimeout time.Duration
	DB                *bbolt.DB

	dbAccess    sync.RWMutex
	resetAccess sync.Mutex

	lifecycleAccess sync.Mutex
	closing         bool
	closed          bool
	closeDone       chan struct{}
	closeErr        error
	cleanupStop     chan struct{}
	workerGroup     sync.WaitGroup
	cleanupStarted  bool

	persistenceAccess    sync.Mutex
	persistenceOperation sync.Mutex
	persistenceWake      chan struct{}
	persistenceStarted   bool
	persistenceClosing   bool
	persistenceCursor    persistenceKind
	pendingEntries       int
	pendingBytes         int
	persistenceErr       error
	persistenceSaver     func(persistenceItem) error
	overloadCount        uint64
	overloadLoggedAt     time.Time
	dnsGeneration        uint64
	rdrcGeneration       uint64
	fakeIPGeneration     uint64
	metadataSave         saveMetadataEntry
	saveFakeIP           map[netip.Addr]saveFakeIPEntry
	saveAddress4         map[string]netip.Addr
	saveAddress6         map[string]netip.Addr
	saveRDRC             map[saveCacheKey]saveRDRCEntry
	saveDNSCache         map[saveCacheKey]saveDNSCacheEntry
}

type saveCacheKey struct {
	TransportName string
	QuestionName  string
	QType         uint16
}

type saveDNSCacheEntry struct {
	rawMessage []byte
	expireAt   time.Time
	sequence   uint64
	generation uint64
	saving     bool
}

type saveRDRCEntry struct {
	expiresAt  time.Time
	sequence   uint64
	generation uint64
	saving     bool
}

type saveFakeIPEntry struct {
	domain     string
	sequence   uint64
	generation uint64
	saving     bool
}

type saveMetadataEntry struct {
	metadata   *adapter.FakeIPMetadata
	sequence   uint64
	generation uint64
	saving     bool
}

func New(ctx context.Context, logger logger.Logger, options option.CacheFileOptions) *CacheFile {
	var path string
	if options.Path != "" {
		path = options.Path
	} else {
		path = "cache.db"
	}
	var cacheIDBytes []byte
	if options.CacheID != "" {
		cacheIDBytes = append([]byte{0}, []byte(options.CacheID)...)
	}
	if options.StoreRDRC {
		deprecated.Report(ctx, deprecated.OptionStoreRDRC)
	}
	var rdrcTimeout time.Duration
	if options.StoreRDRC {
		if options.RDRCTimeout > 0 {
			rdrcTimeout = time.Duration(options.RDRCTimeout)
		} else {
			rdrcTimeout = 7 * 24 * time.Hour
		}
	}
	return &CacheFile{
		ctx:             ctx,
		logger:          logger,
		path:            filemanager.BasePath(ctx, path),
		cacheID:         cacheIDBytes,
		cacheIDText:     options.CacheID,
		storeFakeIP:     options.StoreFakeIP,
		storeRDRC:       options.StoreRDRC,
		storeDNS:        options.StoreDNS,
		rdrcTimeout:     rdrcTimeout,
		closeDone:       make(chan struct{}),
		cleanupStop:     make(chan struct{}),
		persistenceWake: make(chan struct{}, 1),
		saveFakeIP:      make(map[netip.Addr]saveFakeIPEntry),
		saveAddress4:    make(map[string]netip.Addr),
		saveAddress6:    make(map[string]netip.Addr),
		saveRDRC:        make(map[saveCacheKey]saveRDRCEntry),
		saveDNSCache:    make(map[saveCacheKey]saveDNSCacheEntry),
	}
}

func (c *CacheFile) Name() string {
	return "cache-file"
}

func (c *CacheFile) Dependencies() []string {
	return nil
}

func (c *CacheFile) CacheID() string {
	return c.cacheIDText
}

func (c *CacheFile) SetOptimisticTimeout(timeout time.Duration) {
	c.optimisticTimeout = timeout
}

func (c *CacheFile) SetDisableExpire(disableExpire bool) {
	c.disableExpire = disableExpire
}

func (c *CacheFile) Start(stage adapter.StartStage) error {
	c.lifecycleAccess.Lock()
	defer c.lifecycleAccess.Unlock()
	if c.closing || c.closed {
		return os.ErrClosed
	}
	switch stage {
	case adapter.StartStateInitialize:
		err := c.start()
		if err != nil {
			return err
		}
		c.startPersistenceWorker()
	case adapter.StartStateStart:
		c.startCacheCleanup()
	}
	return nil
}

func (c *CacheFile) startCacheCleanup() {
	if c.cleanupStarted {
		return
	}
	var (
		interval    time.Duration
		cleanupFunc func()
	)
	if c.storeDNS {
		c.clearRDRC()
		c.cleanupDNSCache()
		interval = c.optimisticTimeout / 2
		cleanupFunc = c.cleanupDNSCache
	} else if c.storeRDRC {
		c.cleanupRDRC()
		interval = c.rdrcTimeout / 2
		cleanupFunc = c.cleanupRDRC
	}
	if cleanupFunc == nil {
		return
	}
	if interval <= 0 {
		interval = time.Hour
	}
	c.cleanupStarted = true
	c.workerGroup.Add(1)
	go func() {
		defer c.workerGroup.Done()
		c.loopCacheCleanup(interval, cleanupFunc)
	}()
}

func (c *CacheFile) start() error {
	const fileMode = 0o666
	cacheFile, err := filemanager.OpenFile(c.ctx, c.path, os.O_RDWR|os.O_CREATE, fileMode)
	if err != nil {
		return err
	}
	cacheFile.Close()
	options := bbolt.Options{Timeout: time.Second}
	var db *bbolt.DB
	for range 10 {
		db, err = bbolt.Open(c.path, fileMode, &options)
		if err == nil {
			break
		}
		if errors.Is(err, bboltErrors.ErrTimeout) {
			continue
		}
		if E.IsMulti(err, bboltErrors.ErrInvalid, bboltErrors.ErrChecksum, bboltErrors.ErrVersionMismatch) {
			rmErr := filemanager.Remove(c.ctx, c.path)
			if rmErr != nil {
				return err
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	err = filemanager.Chown(c.ctx, c.path)
	if err != nil {
		db.Close()
		return E.Cause(err, "platform chown")
	}
	err = db.Batch(func(tx *bbolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bbolt.Bucket) error {
			if name[0] == 0 {
				return b.ForEachBucket(func(k []byte) error {
					bucketName := string(k)
					if !(common.Contains(bucketNameList, bucketName)) {
						_ = b.DeleteBucket(name)
					}
					return nil
				})
			} else {
				bucketName := string(name)
				if !(common.Contains(bucketNameList, bucketName) || strings.HasPrefix(bucketName, fakeipBucketPrefix)) {
					_ = tx.DeleteBucket(name)
				}
			}
			return nil
		})
	})
	if err != nil {
		db.Close()
		return err
	}
	c.dbAccess.Lock()
	c.DB = db
	c.dbAccess.Unlock()
	return nil
}

func (c *CacheFile) Close() error {
	c.lifecycleAccess.Lock()
	if c.closing || c.closed {
		done := c.closeDone
		c.lifecycleAccess.Unlock()
		<-done
		c.lifecycleAccess.Lock()
		err := c.closeErr
		c.lifecycleAccess.Unlock()
		return err
	}
	c.closing = true
	close(c.cleanupStop)
	c.persistenceAccess.Lock()
	c.persistenceClosing = true
	c.persistenceAccess.Unlock()
	c.notifyPersistenceWorker()
	c.lifecycleAccess.Unlock()

	c.workerGroup.Wait()

	c.dbAccess.Lock()
	var dbErr error
	if c.DB != nil {
		dbErr = c.DB.Close()
		c.DB = nil
	}
	c.dbAccess.Unlock()

	c.persistenceAccess.Lock()
	persistenceErr := c.persistenceErr
	overloadCount := c.overloadCount
	c.persistenceAccess.Unlock()
	if overloadCount > 0 && c.logger != nil {
		c.logger.Warn("cache persistence discarded ", overloadCount, " update(s) because the bounded queue was full")
	}

	c.lifecycleAccess.Lock()
	c.closeErr = E.Errors(persistenceErr, dbErr)
	c.closed = true
	c.closing = false
	close(c.closeDone)
	err := c.closeErr
	c.lifecycleAccess.Unlock()
	return err
}

func (c *CacheFile) view(fn func(tx *bbolt.Tx) error) (err error) {
	return c.withDB(func(db *bbolt.DB) error { return db.View(fn) })
}

func (c *CacheFile) batch(fn func(tx *bbolt.Tx) error) (err error) {
	return c.withDB(func(db *bbolt.DB) error { return db.Batch(fn) })
}

func (c *CacheFile) update(fn func(tx *bbolt.Tx) error) (err error) {
	return c.withDB(func(db *bbolt.DB) error { return db.Update(fn) })
}

func (c *CacheFile) withDB(operation func(db *bbolt.DB) error) (err error) {
	c.dbAccess.RLock()
	db := c.DB
	if db == nil {
		c.dbAccess.RUnlock()
		return os.ErrClosed
	}
	var panicValue any
	func() {
		defer func() { panicValue = recover() }()
		err = operation(db)
	}()
	c.dbAccess.RUnlock()
	if panicValue != nil {
		c.resetDB()
		return E.New("database corrupted: ", panicValue)
	}
	return err
}

func (c *CacheFile) resetDB() {
	c.resetAccess.Lock()
	defer c.resetAccess.Unlock()
	c.dbAccess.Lock()
	defer c.dbAccess.Unlock()
	if c.DB == nil {
		return
	}
	_ = c.DB.Close()
	c.DB = nil
	_ = filemanager.Remove(c.ctx, c.path)
	db, err := bbolt.Open(c.path, 0o666, &bbolt.Options{Timeout: time.Second})
	if err == nil {
		_ = filemanager.Chown(c.ctx, c.path)
		c.DB = db
	}
}

func (c *CacheFile) StoreFakeIP() bool {
	return c.storeFakeIP
}

func (c *CacheFile) LoadMode() string {
	var mode string
	c.view(func(t *bbolt.Tx) error {
		bucket := t.Bucket(bucketMode)
		if bucket == nil {
			return nil
		}
		var modeBytes []byte
		if len(c.cacheID) > 0 {
			modeBytes = bucket.Get(c.cacheID)
		} else {
			modeBytes = bucket.Get(cacheIDDefault)
		}
		mode = string(modeBytes)
		return nil
	})
	return mode
}

func (c *CacheFile) StoreMode(mode string) error {
	return c.batch(func(t *bbolt.Tx) error {
		bucket, err := t.CreateBucketIfNotExists(bucketMode)
		if err != nil {
			return err
		}
		if len(c.cacheID) > 0 {
			return bucket.Put(c.cacheID, []byte(mode))
		} else {
			return bucket.Put(cacheIDDefault, []byte(mode))
		}
	})
}

func (c *CacheFile) bucket(t *bbolt.Tx, key []byte) *bbolt.Bucket {
	if c.cacheID == nil {
		return t.Bucket(key)
	}
	bucket := t.Bucket(c.cacheID)
	if bucket == nil {
		return nil
	}
	return bucket.Bucket(key)
}

func (c *CacheFile) createBucket(t *bbolt.Tx, key []byte) (*bbolt.Bucket, error) {
	if c.cacheID == nil {
		return t.CreateBucketIfNotExists(key)
	}
	bucket, err := t.CreateBucketIfNotExists(c.cacheID)
	if bucket == nil {
		return nil, err
	}
	return bucket.CreateBucketIfNotExists(key)
}

func (c *CacheFile) LoadSelected(group string) string {
	var selected string
	c.view(func(t *bbolt.Tx) error {
		bucket := c.bucket(t, bucketSelected)
		if bucket == nil {
			return nil
		}
		selectedBytes := bucket.Get([]byte(group))
		if len(selectedBytes) > 0 {
			selected = string(selectedBytes)
		}
		return nil
	})
	return selected
}

func (c *CacheFile) StoreSelected(group, selected string) error {
	return c.batch(func(t *bbolt.Tx) error {
		bucket, err := c.createBucket(t, bucketSelected)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(group), []byte(selected))
	})
}

func (c *CacheFile) LoadGroupExpand(group string) (isExpand bool, loaded bool) {
	c.view(func(t *bbolt.Tx) error {
		bucket := c.bucket(t, bucketExpand)
		if bucket == nil {
			return nil
		}
		expandBytes := bucket.Get([]byte(group))
		if len(expandBytes) == 1 {
			isExpand = expandBytes[0] == 1
			loaded = true
		}
		return nil
	})
	return
}

func (c *CacheFile) StoreGroupExpand(group string, isExpand bool) error {
	return c.batch(func(t *bbolt.Tx) error {
		bucket, err := c.createBucket(t, bucketExpand)
		if err != nil {
			return err
		}
		if isExpand {
			return bucket.Put([]byte(group), []byte{1})
		} else {
			return bucket.Put([]byte(group), []byte{0})
		}
	})
}

func (c *CacheFile) LoadRuleSet(tag string) *adapter.SavedBinary {
	var savedSet adapter.SavedBinary
	err := c.view(func(t *bbolt.Tx) error {
		bucket := c.bucket(t, bucketRuleSet)
		if bucket == nil {
			return os.ErrNotExist
		}
		setBinary := bucket.Get([]byte(tag))
		if len(setBinary) == 0 {
			return os.ErrInvalid
		}
		return savedSet.UnmarshalBinary(setBinary)
	})
	if err != nil {
		return nil
	}
	return &savedSet
}

func (c *CacheFile) SaveRuleSet(tag string, set *adapter.SavedBinary) error {
	return c.batch(func(t *bbolt.Tx) error {
		bucket, err := c.createBucket(t, bucketRuleSet)
		if err != nil {
			return err
		}
		setBinary, err := set.MarshalBinary()
		if err != nil {
			return err
		}
		return bucket.Put([]byte(tag), setBinary)
	})
}
