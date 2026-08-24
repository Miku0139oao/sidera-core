package rule

import (
	"context"
	"runtime"
	"sync"
	"time"

	"github.com/Miku0139oao/sidera-core/adapter"
	"github.com/sagernet/sing/common"
)

type RuleSetUpdater struct {
	ctx      context.Context
	cancel   context.CancelFunc
	ruleSets []*RemoteRuleSet
	access   sync.Mutex
	started  bool
	closed   bool
	workers  sync.WaitGroup
}

func NewRuleSetUpdater(ctx context.Context, ruleSets []adapter.RuleSet) *RuleSetUpdater {
	var remoteRuleSets []*RemoteRuleSet
	for _, ruleSet := range ruleSets {
		remoteRuleSet, isRemote := ruleSet.(*RemoteRuleSet)
		if isRemote {
			remoteRuleSets = append(remoteRuleSets, remoteRuleSet)
		}
	}
	if len(remoteRuleSets) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	return &RuleSetUpdater{
		ctx:      ctx,
		cancel:   cancel,
		ruleSets: remoteRuleSets,
	}
}

func (u *RuleSetUpdater) Start() {
	u.access.Lock()
	if u.started || u.closed {
		u.access.Unlock()
		return
	}
	u.started = true
	u.workers.Add(1)
	u.access.Unlock()
	go func() {
		defer u.workers.Done()
		u.loopUpdate()
	}()
}

func (u *RuleSetUpdater) Close() error {
	u.access.Lock()
	if !u.closed {
		u.closed = true
		u.cancel()
	}
	u.access.Unlock()
	u.workers.Wait()
	return nil
}

func (u *RuleSetUpdater) loopUpdate() {
	nextUpdates := make([]time.Time, len(u.ruleSets))
	for i, ruleSet := range u.ruleSets {
		nextUpdates[i] = ruleSet.lastUpdateTime().Add(ruleSet.updateInterval)
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-u.ctx.Done():
			return
		case <-timer.C:
		}
		now := time.Now()
		var updated bool
		for i, ruleSet := range u.ruleSets {
			if now.Before(nextUpdates[i]) {
				continue
			}
			if u.ctx.Err() != nil {
				return
			}
			if ruleSet.updateOnce(u.ctx) {
				updated = true
			}
			nextUpdates[i] = now.Add(ruleSet.updateInterval)
		}
		if updated && common.LowMemory {
			runtime.GC()
		}
		timer.Reset(waitUntilNext(nextUpdates))
	}
}

func waitUntilNext(nextUpdates []time.Time) time.Duration {
	next := nextUpdates[0]
	for _, nextUpdate := range nextUpdates[1:] {
		if nextUpdate.Before(next) {
			next = nextUpdate
		}
	}
	wait := time.Until(next)
	if wait < 0 {
		return 0
	}
	return wait
}
