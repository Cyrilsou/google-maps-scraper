package websitescraper

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

// mxResolver is the DNS shim the rest of the file uses. Tests can swap it.
var mxResolver = net.DefaultResolver

// mxCacheEntry is stored in the process-wide cache. Positive and negative
// results live for different TTLs so a transient DNS hiccup does not poison
// the cache for hours.
type mxCacheEntry struct {
	valid   bool
	expires time.Time
}

var (
	mxCacheMu sync.RWMutex
	mxCache   = map[string]mxCacheEntry{}
)

const (
	mxPositiveTTL = 6 * time.Hour
	mxNegativeTTL = 15 * time.Minute
	mxLookupWait  = 4 * time.Second
)

// ValidateEmails returns a subset of emails whose domain has at least one MX
// record (or, as a fallback, an A/AAAA record — RFC 5321 §5.1 permits a
// domain without MX to still accept mail on its A host).
//
// A process-wide cache keeps the DNS chatter bounded: a campaign that scrapes
// 10 000 leads typically hits ~300 unique domains, so the cache converts most
// of those 10 000 email validations into in-memory lookups after the first
// batch.
func ValidateEmails(ctx context.Context, emails []string) []string {
	if len(emails) == 0 {
		return emails
	}

	// Group by domain — one DNS lookup per domain, not per email.
	byDomain := map[string][]string{}

	for _, e := range emails {
		at := strings.LastIndex(e, "@")
		if at <= 0 || at == len(e)-1 {
			continue
		}

		d := strings.ToLower(e[at+1:])
		byDomain[d] = append(byDomain[d], e)
	}

	kept := make([]string, 0, len(emails))

	for domain, es := range byDomain {
		if domainHasMX(ctx, domain) {
			kept = append(kept, es...)
		}
	}

	return kept
}

func domainHasMX(ctx context.Context, domain string) bool {
	// Fast path: if the caller's ctx is already done, skip the lookup AND
	// skip the cache write. A "valid=false" cache entry for a cancelled
	// lookup would poison future calls for this domain for mxNegativeTTL.
	if err := ctx.Err(); err != nil {
		return false
	}

	mxCacheMu.RLock()
	if ent, ok := mxCache[domain]; ok && time.Now().Before(ent.expires) {
		mxCacheMu.RUnlock()
		return ent.valid
	}
	mxCacheMu.RUnlock()

	lookupCtx, cancel := context.WithTimeout(ctx, mxLookupWait)
	defer cancel()

	valid := false
	transient := false

	if mxs, err := mxResolver.LookupMX(lookupCtx, domain); err == nil && len(mxs) > 0 {
		valid = true
	} else if ips, err := mxResolver.LookupIPAddr(lookupCtx, domain); err == nil && len(ips) > 0 {
		// RFC 5321 fallback: if there's no MX but an A record exists, mail
		// can still be delivered to that host. Most small-business domains
		// behave this way.
		valid = true
	} else if lookupCtx.Err() != nil {
		// Transient error (ctx expired mid-lookup, network hiccup). Do not
		// cache — a retry on the next call might succeed.
		transient = true
	}

	if transient {
		return false
	}

	ttl := mxPositiveTTL
	if !valid {
		ttl = mxNegativeTTL
	}

	mxCacheMu.Lock()
	mxCache[domain] = mxCacheEntry{valid: valid, expires: time.Now().Add(ttl)}
	mxCacheMu.Unlock()

	return valid
}

// MXCacheSize returns the number of domains currently memoised. Useful for
// log metrics, tests, and cache eviction decisions.
func MXCacheSize() int {
	mxCacheMu.RLock()
	defer mxCacheMu.RUnlock()

	return len(mxCache)
}

// ClearMXCache wipes the cache. Mostly useful in tests.
func ClearMXCache() {
	mxCacheMu.Lock()
	defer mxCacheMu.Unlock()

	mxCache = map[string]mxCacheEntry{}
}
