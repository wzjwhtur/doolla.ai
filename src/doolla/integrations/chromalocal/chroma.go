// Package chromalocal opens a Chroma directory and its native all-MiniLM-L6-v2 embedding model.
package chromalocal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	chroma "github.com/amikos-tech/chroma-go/pkg/api/v2"
	"github.com/amikos-tech/chroma-go/pkg/embeddings/ort"
	doolla "doolla.ai/sdk"
	"doolla.ai/sdk/integrations/memory"
)

// Store owns any native runtime and embedding session opened for this collection.
type Store struct {
	doolla.MemoryStore
	runtime   io.Closer
	embedding io.Closer
}

func (s *Store) Close() error {
	var errs []error
	if s.embedding != nil {
		errs = append(errs, s.embedding.Close())
	}
	if s.runtime != nil {
		errs = append(errs, s.runtime.Close())
	}
	return errors.Join(errs...)
}

// Open can be used as the CLI memory opener or called directly by an application.
// Supply a directory for a local index or an endpoint for a remote collection.
func Open(ctx context.Context, config memory.Config) (doolla.MemoryStore, error) {
	if config.Provider != "chroma" && config.Provider != "chromadb" {
		return memory.Open(ctx, config)
	}
	store := &Store{}
	success := false
	defer func() {
		if !success {
			_ = store.Close()
		}
	}()
	if config.Directory == "" && config.Endpoint == "" {
		config.Directory = "./chroma_memory"
	}
	if config.Directory != "" {
		if config.Endpoint != "" {
			return nil, errors.New("select a Chroma directory or an HTTP endpoint")
		}
		// The native runtime reports the configured port verbatim, including zero.
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		if err := listener.Close(); err != nil {
			return nil, err
		}
		client, err := chroma.NewPersistentClient(chroma.WithPersistentPath(config.Directory), chroma.WithPersistentListenAddress("127.0.0.1"), chroma.WithPersistentPort(port))
		if err != nil {
			return nil, fmt.Errorf("open local Chroma runtime: %w", err)
		}
		store.runtime = client
		config.Endpoint = fmt.Sprintf("http://127.0.0.1:%d", port)
		config.Provision = true
	}
	if config.Embed == nil {
		model, _, err := ort.NewDefaultEmbeddingFunction()
		if err != nil {
			return nil, fmt.Errorf("open Chroma default embedding: %w", err)
		}
		store.embedding = model
		config.Embed = func(ctx context.Context, text string) ([]float32, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			vec, err := model.EmbedQuery(ctx, text)
			if err != nil {
				return nil, err
			}
			return vec.ContentAsFloat32(), ctx.Err()
		}
	}
	var err error
	store.MemoryStore, err = memory.Open(ctx, config)
	if err != nil {
		return nil, err
	}
	success = true
	return store, nil
}
