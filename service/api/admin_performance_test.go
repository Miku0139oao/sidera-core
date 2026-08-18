package api

import (
	"fmt"
	"testing"
)

func TestAdminAccountViewsAllocationBudget(t *testing.T) {
	const accountCount = 2000
	a, active := newAdminAccountBenchmark(accountCount, 20)
	allocations := testing.AllocsPerRun(20, func() {
		a.storeAccess.RLock()
		accountUsage := a.allAccountUsageLocked(active)
		a.storeAccess.RUnlock()
		views := a.accountViews(active, accountUsage)
		if len(views) != accountCount {
			t.Fatalf("unexpected account count: %d", len(views))
		}
	})
	if allocations > 20 {
		t.Fatalf("account view allocation budget exceeded: %.1f > 20", allocations)
	}
}

func BenchmarkAdminAccountViews(b *testing.B) {
	const accountCount = 2000
	a, active := newAdminAccountBenchmark(accountCount, 20)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		a.storeAccess.RLock()
		accountUsage := a.allAccountUsageLocked(active)
		a.storeAccess.RUnlock()
		views := a.accountViews(active, accountUsage)
		if len(views) != accountCount {
			b.Fatalf("unexpected account count: %d", len(views))
		}
	}
}

func BenchmarkAdminMembershipPolicyUsage(b *testing.B) {
	a, active := newAdminAccountBenchmark(2000, 20)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		a.storeAccess.RLock()
		usage := a.accountUsageLocked(active)
		a.storeAccess.RUnlock()
		if len(usage) != 0 {
			b.Fatalf("unexpected global policy usage: %d", len(usage))
		}
	}
}

func newAdminAccountBenchmark(accountCount int, inboundCount int) (*adminAPI, map[string]adminUsage) {
	a := &adminAPI{store: adminStore{
		Accounts: make(map[string]*adminAccount, accountCount),
		Inbounds: make(map[string]*adminInboundStore, inboundCount),
	}}
	active := make(map[string]adminUsage, accountCount)
	for index := range inboundCount {
		tag := fmt.Sprintf("inbound-%02d", index)
		a.store.Inbounds[tag] = &adminInboundStore{Users: make([]*adminUser, 0, accountCount/inboundCount)}
	}
	for index := range accountCount {
		identifier := fmt.Sprintf("account-%04d", index)
		name := fmt.Sprintf("user-%04d", index)
		tag := fmt.Sprintf("inbound-%02d", index%inboundCount)
		a.store.Accounts[identifier] = &adminAccount{ID: identifier, Name: name, PolicyScope: adminAccountPolicyMembership}
		a.store.Inbounds[tag].Users = append(a.store.Inbounds[tag].Users, &adminUser{AccountID: identifier, Name: name})
		active[adminUserKey(tag, name)] = adminUsage{Upload: int64(index), Download: int64(index), Connections: 1}
	}
	return a, active
}
