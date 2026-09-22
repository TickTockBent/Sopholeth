package cluster

import "sopholeth/internal/storage"

// Subscribe observes storage acceptance for both client and replicated writes.
func (cn *ClusterNode) Subscribe() (storage.Snapshot, *storage.Subscription, error) {
	if store, ok := cn.store.(interface {
		Subscribe() (storage.Snapshot, *storage.Subscription, error)
	}); ok {
		return store.Subscribe()
	}
	return storage.Snapshot{}, nil, storage.ErrStoreClosed
}
