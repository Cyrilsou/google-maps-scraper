package gmaps

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ProxyBlockStats tracks how often each proxy has hit a CAPTCHA / block page.
// It is a pure in-memory counter exposed for monitoring and for manual
// rotation decisions — scrapemate picks the next proxy on its own, so this
// package does not try to force a specific proxy per job.
//
// The exported snapshot is safe to serialise as JSON (see /metrics handlers).
type ProxyBlockStats struct {
	mu     sync.Mutex
	counts map[string]*proxyStat

	totalBlocks atomic.Int64
	totalOK     atomic.Int64
}

type proxyStat struct {
	Blocks       int64     `json:"blocks"`
	OK           int64     `json:"ok"`
	LastBlockedAt time.Time `json:"last_blocked_at,omitempty"`
}

// DefaultProxyStats is the shared registry used by the scraping paths. A
// global is acceptable here because the counters are trivially concurrent-
// safe and the data is purely observational.
var DefaultProxyStats = NewProxyBlockStats()

// NewProxyBlockStats returns a fresh tracker.
func NewProxyBlockStats() *ProxyBlockStats {
	return &ProxyBlockStats{counts: make(map[string]*proxyStat)}
}

// RecordBlock increments the block counter for proxy (use "" for direct).
func (p *ProxyBlockStats) RecordBlock(proxy string) {
	if p == nil {
		return
	}

	p.totalBlocks.Add(1)

	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.counts[proxy]
	if !ok {
		s = &proxyStat{}
		p.counts[proxy] = s
	}

	s.Blocks++
	s.LastBlockedAt = time.Now()
}

// RecordOK increments the success counter for proxy.
func (p *ProxyBlockStats) RecordOK(proxy string) {
	if p == nil {
		return
	}

	p.totalOK.Add(1)

	p.mu.Lock()
	defer p.mu.Unlock()

	s, ok := p.counts[proxy]
	if !ok {
		s = &proxyStat{}
		p.counts[proxy] = s
	}

	s.OK++
}

// ProxySnapshot is the serialisable view of a single proxy's health.
type ProxySnapshot struct {
	Proxy         string    `json:"proxy"`
	Blocks        int64     `json:"blocks"`
	OK            int64     `json:"ok"`
	LastBlockedAt time.Time `json:"last_blocked_at,omitempty"`
}

// Snapshot returns the current state sorted by block count (descending). Use
// this in /metrics or a debug handler to spot proxies that keep getting
// flagged — typical reason to rotate them out.
func (p *ProxyBlockStats) Snapshot() []ProxySnapshot {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	out := make([]ProxySnapshot, 0, len(p.counts))
	for k, s := range p.counts {
		out = append(out, ProxySnapshot{
			Proxy:         k,
			Blocks:        s.Blocks,
			OK:            s.OK,
			LastBlockedAt: s.LastBlockedAt,
		})
	}
	p.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Blocks != out[j].Blocks {
			return out[i].Blocks > out[j].Blocks
		}

		return out[i].Proxy < out[j].Proxy
	})

	return out
}

// Totals returns the cumulative (blocks, ok) counts across all proxies.
func (p *ProxyBlockStats) Totals() (blocks, ok int64) {
	if p == nil {
		return 0, 0
	}

	return p.totalBlocks.Load(), p.totalOK.Load()
}
