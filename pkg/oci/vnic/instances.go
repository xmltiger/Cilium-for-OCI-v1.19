// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package vnic

import (
	"context"
	"log/slog"
	"maps"

	"github.com/cilium/cilium/pkg/ipam"
	ipamTypes "github.com/cilium/cilium/pkg/ipam/types"
	v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/oci/vnic/types"
	"github.com/cilium/cilium/pkg/time"

	"github.com/oracle/oci-go-sdk/v65/core"
)

type API interface {
	GetVCNs(context.Context) (ipamTypes.VirtualNetworkMap, error)
	GetSubnets(context.Context) (ipamTypes.SubnetMap, error)
	GetInstances(context.Context, ipamTypes.VirtualNetworkMap) (*ipamTypes.InstanceMap, error)
	GetInstance(context.Context, ipamTypes.VirtualNetworkMap, string) (*ipamTypes.Instance, error)
	GetVNIC(context.Context, core.VnicAttachment, ipamTypes.VirtualNetworkMap) (*types.VNIC, error)
	AttachVNIC(context.Context, string, string, []string, map[string]string) (string, error)
	WaitVNICAttached(context.Context, string) (core.VnicAttachment, error)
	DetachVNIC(context.Context, string) error
	AssignPrivateIPAddresses(context.Context, string, int) ([]string, error)
	UnassignPrivateIPAddresses(context.Context, string, []string) error
}

type InstancesManager struct {
	logger *slog.Logger
	api    API

	resyncLock lock.RWMutex
	mutex      lock.RWMutex
	instances  *ipamTypes.InstanceMap
	subnets    ipamTypes.SubnetMap
	vcns       ipamTypes.VirtualNetworkMap
}

func NewInstancesManager(logger *slog.Logger, api API) *InstancesManager {
	return &InstancesManager{
		logger:    logger.With(logfields.LogSubsys, "oci-vnic-instances"),
		api:       api,
		instances: ipamTypes.NewInstanceMap(),
		subnets:   ipamTypes.SubnetMap{},
		vcns:      ipamTypes.VirtualNetworkMap{},
	}
}

func (m *InstancesManager) CreateNode(obj *v2.CiliumNode, node *ipam.Node) ipam.NodeOperations {
	return &Node{
		logger:     m.logger,
		k8sObj:     obj,
		manager:    m,
		node:       node,
		instanceID: node.InstanceID(),
		vnics:      map[string]types.VNIC{},
	}
}

func (m *InstancesManager) HasInstance(instanceID string) bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.instances.Exists(instanceID)
}

func (m *InstancesManager) DeleteInstance(instanceID string) {
	m.mutex.Lock()
	m.instances.Delete(instanceID)
	m.mutex.Unlock()
}

func (m *InstancesManager) GetPoolQuota() ipamTypes.PoolQuotaMap {
	result := ipamTypes.PoolQuotaMap{}
	for id, subnet := range m.GetSubnets() {
		result[ipamTypes.PoolID(id)] = ipamTypes.PoolQuota{
			AvailabilityZone: subnet.AvailabilityZone,
			AvailableIPs:     subnet.AvailableAddresses,
		}
	}
	return result
}

func (m *InstancesManager) Resync(ctx context.Context) time.Time {
	m.resyncLock.Lock()
	defer m.resyncLock.Unlock()
	return m.resync(ctx, "")
}

func (m *InstancesManager) InstanceSync(ctx context.Context, instanceID string) time.Time {
	m.resyncLock.RLock()
	defer m.resyncLock.RUnlock()
	return m.resync(ctx, instanceID)
}

func (m *InstancesManager) resync(ctx context.Context, instanceID string) time.Time {
	start := time.Now()
	vcns, err := m.api.GetVCNs(ctx)
	if err != nil {
		m.logger.Warn("Unable to synchronize OCI VCNs", logfields.Error, err)
		return time.Time{}
	}
	subnets, err := m.api.GetSubnets(ctx)
	if err != nil {
		m.logger.Warn("Unable to synchronize OCI subnets", logfields.Error, err)
		return time.Time{}
	}

	if instanceID == "" {
		instances, err := m.api.GetInstances(ctx, vcns)
		if err != nil {
			m.logger.Warn("Unable to synchronize OCI instances", logfields.Error, err)
			return time.Time{}
		}
		if instances == nil {
			instances = ipamTypes.NewInstanceMap()
		}
		m.mutex.Lock()
		m.instances = instances
		m.vcns = vcns
		m.subnets = subnets
		m.mutex.Unlock()
		m.logger.Info("Synchronized OCI VNIC information",
			"instances", instances.NumInstances(),
			"vcns", len(vcns),
			"subnets", len(subnets))
		return start
	}

	instance, err := m.api.GetInstance(ctx, vcns, instanceID)
	if err != nil {
		m.logger.Warn("Unable to synchronize OCI instance", logfields.InstanceID, instanceID, logfields.Error, err)
		return time.Time{}
	}
	m.mutex.Lock()
	m.instances.UpdateInstance(instanceID, instance)
	m.vcns = vcns
	m.subnets = subnets
	m.mutex.Unlock()
	return start
}

func (m *InstancesManager) GetSubnets() ipamTypes.SubnetMap {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	result := make(ipamTypes.SubnetMap, len(m.subnets))
	for id, subnet := range m.subnets {
		result[id] = subnet.DeepCopy()
	}
	return result
}

func (m *InstancesManager) GetSubnet(id string) *ipamTypes.Subnet {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	if subnet := m.subnets[id]; subnet != nil {
		return subnet.DeepCopy()
	}
	return nil
}

func (m *InstancesManager) ForeachInstance(instanceID string, fn ipamTypes.InterfaceIterator) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	m.instances.ForeachInterface(instanceID, fn)
}

func (m *InstancesManager) UpdateVNIC(instanceID string, vnic *types.VNIC) {
	m.mutex.Lock()
	m.instances.Update(instanceID, ipamTypes.InterfaceRevision{Resource: vnic})
	m.mutex.Unlock()
}

func (m *InstancesManager) FindSubnet(spec types.Spec, toAllocate int) *ipamTypes.Subnet {
	subnets := m.GetSubnets()
	allowedIDs := map[string]struct{}{}
	for _, id := range spec.SubnetIDs {
		allowedIDs[id] = struct{}{}
	}

	var best *ipamTypes.Subnet
	for _, subnet := range subnets {
		if spec.VCNID != "" && subnet.VirtualNetworkID != spec.VCNID {
			continue
		}
		if len(allowedIDs) > 0 {
			if _, ok := allowedIDs[subnet.ID]; !ok {
				continue
			}
		}
		// Regional subnets have no availability domain and are valid for all
		// ADs in their region.
		if subnet.AvailabilityZone != "" &&
			spec.AvailabilityDomain != "" &&
			subnet.AvailabilityZone != spec.AvailabilityDomain {
			continue
		}
		if subnet.AvailableAddresses < toAllocate {
			continue
		}
		if !subnet.Tags.Match(ipamTypes.Tags(spec.SubnetTags)) {
			continue
		}
		if best == nil || best.AvailableAddresses < subnet.AvailableAddresses {
			best = subnet
		}
	}
	return best
}

func (m *InstancesManager) VCNs() ipamTypes.VirtualNetworkMap {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	result := make(ipamTypes.VirtualNetworkMap, len(m.vcns))
	maps.Copy(result, m.vcns)
	return result
}
