package api

import (
	"sync"
)

// ---------------------------------------------------------------------------
// Stats buffer
// ---------------------------------------------------------------------------

type pkgKey struct {
	Name    string
	PkgType string
}

type MarketplaceStatsBuffer struct {
	mu       sync.Mutex
	installs map[pkgKey]int64
	calls    map[pkgKey]int64
	sessions int64
	messages int64
}

func NewMarketplaceStatsBuffer() *MarketplaceStatsBuffer {
	return &MarketplaceStatsBuffer{
		installs: make(map[pkgKey]int64),
		calls:    make(map[pkgKey]int64),
	}
}

func (b *MarketplaceStatsBuffer) RecordInstall(name, pkgType string) {
	b.mu.Lock()
	b.installs[pkgKey{name, pkgType}]++
	b.mu.Unlock()
}

func (b *MarketplaceStatsBuffer) RecordCall(name, pkgType string) {
	b.mu.Lock()
	b.calls[pkgKey{name, pkgType}]++
	b.mu.Unlock()
}

func (b *MarketplaceStatsBuffer) RecordSession() {
	b.mu.Lock()
	b.sessions++
	b.mu.Unlock()
}

func (b *MarketplaceStatsBuffer) RecordMessage() {
	b.mu.Lock()
	b.messages++
	b.mu.Unlock()
}

func (b *MarketplaceStatsBuffer) Flush() (installs, calls map[pkgKey]int64, sessions, messages int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.installs) == 0 && len(b.calls) == 0 && b.sessions == 0 && b.messages == 0 {
		return nil, nil, 0, 0
	}
	installs, calls = b.installs, b.calls
	sessions, messages = b.sessions, b.messages
	b.installs = make(map[pkgKey]int64)
	b.calls = make(map[pkgKey]int64)
	b.sessions = 0
	b.messages = 0
	return
}

