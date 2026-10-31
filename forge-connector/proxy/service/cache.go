package service

import (
	"sync"
	"time"
)

// cache is a small concurrency-safe TTL map with a size cap. A full cache
// drops expired entries first and then refuses new ones, so callers cannot
// grow it without bound.
type cache[V any] struct {
	mu      sync.Mutex
	now     func() time.Time
	max     int
	entries map[string]cacheEntry[V]
}

type cacheEntry[V any] struct {
	value   V
	expires time.Time
}

func newCache[V any](now func() time.Time, max int) *cache[V] {
	return &cache[V]{now: now, max: max, entries: make(map[string]cacheEntry[V])}
}

func (c *cache[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if ok && !c.now().Before(e.expires) {
		delete(c.entries, key)
		ok = false
	}
	return e.value, ok
}

// set stores value for ttl. A zero or negative ttl stores nothing.
func (c *cache[V]) set(key string, value V, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.max {
		for k, e := range c.entries {
			if !now.Before(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= c.max {
			return
		}
	}
	c.entries[key] = cacheEntry[V]{value: value, expires: now.Add(ttl)}
}

func (c *cache[V]) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}
