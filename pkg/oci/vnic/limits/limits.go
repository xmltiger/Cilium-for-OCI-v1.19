// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package limits

import (
	"context"
	"sync"

	ipamTypes "github.com/cilium/cilium/pkg/ipam/types"
)

type API interface {
	GetInstanceTypeLimits(context.Context) (map[string]ipamTypes.Limits, error)
}

var (
	mu         sync.RWMutex
	shapeLimit = map[string]ipamTypes.Limits{}
)

func UpdateFromAPI(ctx context.Context, api API) error {
	limits, err := api.GetInstanceTypeLimits(ctx)
	if err != nil {
		return err
	}
	mu.Lock()
	shapeLimit = limits
	mu.Unlock()
	return nil
}

func Get(shape string) (ipamTypes.Limits, bool) {
	mu.RLock()
	limit, ok := shapeLimit[shape]
	mu.RUnlock()
	return limit, ok
}

// Set allows tests and operators with a newly introduced shape to provide a
// validated limit without relying on a global hard-coded shape table.
func Set(shape string, limit ipamTypes.Limits) {
	mu.Lock()
	shapeLimit[shape] = limit
	mu.Unlock()
}
