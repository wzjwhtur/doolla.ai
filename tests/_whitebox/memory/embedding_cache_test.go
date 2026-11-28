package memory

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestEmbeddingCacheEvictionAndVectorOwnership(t *testing.T) {
	var misses []string
	cached := cacheEmbeddings(func(_ context.Context, text string) ([]float32, error) {
		misses = append(misses, text)
		return []float32{float32(len(misses))}, nil
	}, 2, 1)
	read := func(key string, want float32) []float32 {
		t.Helper()
		v, err := cached(t.Context(), key)
		if err != nil || !slices.Equal(v, []float32{want}) {
			t.Fatalf("%s: got %v, %v; want [%v]", key, v, err, want)
		}
		return v
	}

	read("a", 1)[0] = 99 // A caller may mutate either a miss or a hit.
	read("b", 2)
	read("a", 1)[0] = 88 // Refresh a so the next insertion evicts b.
	read("c", 3)
	read("a", 1)
	read("b", 4)
	if !slices.Equal(misses, []string{"a", "b", "c", "b"}) {
		t.Fatalf("unexpected backend requests: %v", misses)
	}
}

func TestEmbeddingCacheRetriesAndCancellation(t *testing.T) {
	failure := errors.New("embedding unavailable")
	calls := 0
	cached := cacheEmbeddings(func(context.Context, string) ([]float32, error) {
		calls++
		switch calls {
		case 1:
			return nil, failure
		case 2:
			return []float32{9}, nil // The downstream store validates dimensions.
		default:
			return []float32{1, 2}, nil
		}
	}, 1, 2)
	ctx := t.Context()
	if _, err := cached(ctx, "term"); !errors.Is(err, failure) {
		t.Fatalf("backend error = %v", err)
	}
	if v, err := cached(ctx, "term"); err != nil || !slices.Equal(v, []float32{9}) {
		t.Fatalf("unexpected second backend result: %v, %v", v, err)
	}
	if v, err := cached(ctx, "term"); err != nil || !slices.Equal(v, []float32{1, 2}) {
		t.Fatalf("expected a valid vector on retry: %v, %v", v, err)
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	for _, key := range []string{"term", "missing"} {
		if v, err := cached(stopped, key); !errors.Is(err, context.Canceled) || v != nil {
			t.Errorf("cancelled lookup %q returned %v, %v", key, v, err)
		}
	}
	if calls != 3 {
		t.Fatalf("expected three backend requests, got %d", calls)
	}
}
