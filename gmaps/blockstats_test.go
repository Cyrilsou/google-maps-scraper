package gmaps

import (
	"sync"
	"testing"
)

func TestProxyBlockStatsRecordAndSnapshot(t *testing.T) {
	s := NewProxyBlockStats()

	s.RecordOK("proxy-a")
	s.RecordOK("proxy-a")
	s.RecordBlock("proxy-a")
	s.RecordBlock("proxy-b")
	s.RecordBlock("proxy-b")
	s.RecordBlock("proxy-b")

	blocks, ok := s.Totals()
	if blocks != 4 {
		t.Fatalf("want 4 blocks total, got %d", blocks)
	}

	if ok != 2 {
		t.Fatalf("want 2 ok total, got %d", ok)
	}

	snap := s.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("want 2 proxies in snapshot, got %d", len(snap))
	}

	// proxy-b must come first (3 blocks > 1 block).
	if snap[0].Proxy != "proxy-b" || snap[0].Blocks != 3 {
		t.Fatalf("snapshot not sorted by blocks desc: got %+v", snap[0])
	}

	if snap[1].Proxy != "proxy-a" || snap[1].Blocks != 1 || snap[1].OK != 2 {
		t.Fatalf("second snapshot entry unexpected: got %+v", snap[1])
	}
}

func TestProxyBlockStatsConcurrentSafe(t *testing.T) {
	s := NewProxyBlockStats()

	const workers = 20
	const perWorker = 100

	var wg sync.WaitGroup

	wg.Add(workers)

	for i := 0; i < workers; i++ {
		go func(id int) {
			defer wg.Done()

			for j := 0; j < perWorker; j++ {
				if j%3 == 0 {
					s.RecordBlock("p")
				} else {
					s.RecordOK("p")
				}
			}
		}(i)
	}

	wg.Wait()

	blocks, ok := s.Totals()

	total := blocks + ok
	want := int64(workers * perWorker)

	if total != want {
		t.Fatalf("concurrency: want %d recorded total, got %d (blocks=%d ok=%d)", want, total, blocks, ok)
	}
}

func TestProxyBlockStatsNil(t *testing.T) {
	var s *ProxyBlockStats

	// All methods must be safe on a nil receiver so test code can swap the
	// tracker out without guards at every call site.
	s.RecordBlock("x")
	s.RecordOK("x")

	if snap := s.Snapshot(); snap != nil {
		t.Fatalf("nil Snapshot should be nil, got %v", snap)
	}

	if blocks, ok := s.Totals(); blocks != 0 || ok != 0 {
		t.Fatalf("nil Totals should be 0/0, got %d/%d", blocks, ok)
	}
}
