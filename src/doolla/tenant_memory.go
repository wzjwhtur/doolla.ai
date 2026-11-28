// Package-level tenant routing is independent of each database driver's
// filtering support. The host provisions physically separate collections,
// namespaces or stores and only its identity resolver supplies the tenant.
package doolla

import (
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"time"
)

const tenantClaim = "tenant_id"

// TenantUser binds a verified tenant and subject to a globally distinct user ID.
// Use this in the authenticated UserResolver, never with client chat metadata.
// History, artifacts and HTTP replay already use User.ID as their boundary.
func TenantUser(tenant string, u User) (User, error) {
	if tenant == "" || u.ID == "" {
		return User{}, errors.New("verified tenant and subject required")
	}
	u.Metadata = maps.Clone(u.Metadata)
	if u.Metadata == nil {
		u.Metadata = map[string]any{}
	}
	u.Metadata[tenantClaim] = tenant
	u.Metadata["tenant_subject"] = u.ID
	u.ID = tenantPrincipal(tenant, u.ID)
	return u, nil
}

func tenantPrincipal(tenant, subject string) string {
	enc := base64.RawURLEncoding
	return "tenant:" + enc.EncodeToString([]byte(tenant)) + "." + enc.EncodeToString([]byte(subject))
}

type TenantMemoryStore struct{ stores map[string]MemoryStore }

// NewTenantMemoryStore routes every operation to a preconfigured store.
// Give each tenant its own physical index/namespace, even when using one server.
// The owner retains responsibility for closing those stores at shutdown.
func NewTenantMemoryStore(stores map[string]MemoryStore) (*TenantMemoryStore, error) {
	if len(stores) == 0 {
		return nil, errors.New("tenant memory mapping is empty")
	}
	for tenant, s := range stores {
		if tenant == "" || s == nil {
			return nil, errors.New("tenant memory requires a named store")
		}
	}
	return &TenantMemoryStore{stores: maps.Clone(stores)}, nil
}

// ForUser also supports administrative SDK calls outside an Agent turn.
func (s *TenantMemoryStore) ForUser(u User) (MemoryStore, error) {
	tenant, _ := u.Metadata[tenantClaim].(string)
	subject, _ := u.Metadata["tenant_subject"].(string)
	if tenant != "" && subject != "" && u.ID == tenantPrincipal(tenant, subject) {
		if store := s.stores[tenant]; store != nil {
			return store, nil
		}
	}
	return nil, errors.New("unknown or unverified memory tenant")
}
func (s *TenantMemoryStore) current(ctx context.Context) (MemoryStore, error) {
	t, _ := CurrentTurn(ctx)
	return s.ForUser(t.User)
}
func (s *TenantMemoryStore) Save(ctx context.Context, e MemoryEntry) error {
	store, err := s.current(ctx)
	if err != nil {
		return err
	}
	return store.Save(ctx, e)
}
func (s *TenantMemoryStore) Search(ctx context.Context, q MemoryQuery) ([]MemoryMatch, error) {
	store, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return store.Search(ctx, q)
}
func (s *TenantMemoryStore) Recent(ctx context.Context, k string, n int) ([]MemoryEntry, error) {
	store, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return store.Recent(ctx, k, n)
}
func (s *TenantMemoryStore) Delete(ctx context.Context, k, id string) (bool, error) {
	store, err := s.current(ctx)
	if err != nil {
		return false, err
	}
	return store.Delete(ctx, k, id)
}
func (s *TenantMemoryStore) Clear(ctx context.Context, k string, before time.Time) (int, error) {
	store, err := s.current(ctx)
	if err != nil {
		return 0, err
	}
	return store.Clear(ctx, k, before)
}
