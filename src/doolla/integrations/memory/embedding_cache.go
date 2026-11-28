package memory

import (
	"container/list"
	"context"
	"crypto/sha256"
	"slices"
	"sync"
)

type cachedVector struct {
	key    [32]byte
	vector []float32
}

// Each opened store owns its own model-specific LRU. Hashing only the key
// avoids retaining raw questions. No network call holds the cache lock.
func cacheEmbeddings(embed func(context.Context, string) ([]float32, error), capacity, dimension int) func(context.Context, string) ([]float32, error) {
	var mu sync.Mutex
	lru := list.New()
	byKey := make(map[[32]byte]*list.Element)
	return func(ctx context.Context, input string) ([]float32, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := sha256.Sum256([]byte(input))
		mu.Lock()
		if e := byKey[key]; e != nil {
			lru.MoveToFront(e)
			v := e.Value.(cachedVector).vector
			mu.Unlock()
			return slices.Clone(v), nil
		}
		mu.Unlock()
		v, err := embed(ctx, input)
		if err != nil || len(v) != dimension {
			return v, err
		}
		mu.Lock()
		defer mu.Unlock()
		if e := byKey[key]; e != nil {
			lru.MoveToFront(e)
		} else {
			byKey[key] = lru.PushFront(cachedVector{key, slices.Clone(v)})
			if lru.Len() > capacity {
				e := lru.Back()
				delete(byKey, e.Value.(cachedVector).key)
				lru.Remove(e)
			}
		}
		return v, nil
	}
}
