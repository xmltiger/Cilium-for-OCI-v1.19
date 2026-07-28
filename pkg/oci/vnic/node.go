// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package vnic

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cilium/cilium/pkg/defaults"
	"github.com/cilium/cilium/pkg/ipam"
	"github.com/cilium/cilium/pkg/ipam/stats"
	ipamTypes "github.com/cilium/cilium/pkg/ipam/types"
	v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/oci/vnic/limits"
	"github.com/cilium/cilium/pkg/oci/vnic/types"
)

const (
	unableToDetermineLimits = "unableToDetermineLimits"
	unableToFindSubnet      = "unableToFindSubnet"
	unableToAttachVNIC      = "unableToAttachVNIC"
	unableToAllocateIPs     = "unableToAllocateIPs"
)

type ipamNodeActions interface {
	InstanceID() string
}

type Node struct {
	logger *slog.Logger
	node   ipamNodeActions

	mutex lock.RWMutex
	vnics map[string]types.VNIC

	k8sObj     *v2.CiliumNode
	manager    *InstancesManager
	instanceID string
}

func (n *Node) UpdatedNode(obj *v2.CiliumNode) {
	n.mutex.Lock()
	n.k8sObj = obj
	n.mutex.Unlock()
}

func (n *Node) PopulateStatusFields(resource *v2.CiliumNode) {
	resource.Status.OCI.VNICs = map[string]types.VNIC{}
	n.manager.ForeachInstance(n.node.InstanceID(),
		func(_, interfaceID string, revision ipamTypes.InterfaceRevision) error {
			vnic, ok := revision.Resource.(*types.VNIC)
			if ok {
				resource.Status.OCI.VNICs[interfaceID] = *vnic.DeepCopy()
			}
			return nil
		})
}

func (n *Node) CreateInterface(ctx context.Context, allocation *ipam.AllocationAction, scopedLog *slog.Logger) (int, string, error) {
	instanceLimits, ok := n.getLimits()
	if !ok {
		return 0, unableToDetermineLimits, errors.New("unable to determine OCI VNIC limits")
	}

	n.mutex.RLock()
	resource := n.k8sObj.DeepCopy()
	n.mutex.RUnlock()

	toAllocate := min(allocation.IPv4.MaxIPsToAllocate, instanceLimits.IPv4)
	if toAllocate <= 0 {
		return 0, "", nil
	}
	subnet := n.manager.FindSubnet(resource.Spec.OCI, toAllocate+1)
	if subnet == nil {
		return 0, unableToFindSubnet, fmt.Errorf(
			"no matching OCI subnet with capacity for %d pod IPs (VCN=%s subnets=%v tags=%v)",
			toAllocate, resource.Spec.OCI.VCNID, resource.Spec.OCI.SubnetIDs, resource.Spec.OCI.SubnetTags)
	}
	allocation.PoolID = ipamTypes.PoolID(subnet.ID)

	scopedLog = scopedLog.With(
		"subnetID", subnet.ID,
		logfields.ToAllocate, toAllocate,
		logfields.InstanceID, n.instanceID,
	)
	attachmentID, err := n.manager.api.AttachVNIC(
		ctx,
		n.instanceID,
		subnet.ID,
		resource.Spec.OCI.NetworkSecurityGroups,
		map[string]string{
			"io.cilium/managed": "true",
			"io.cilium/node":    resource.Name,
		},
	)
	if err != nil {
		return 0, unableToAttachVNIC, err
	}

	cleanup := func(cause error) (int, string, error) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		detachErr := n.manager.api.DetachVNIC(cleanupCtx, attachmentID)
		return 0, unableToAttachVNIC, errors.Join(cause, detachErr)
	}

	attachment, err := n.manager.api.WaitVNICAttached(ctx, attachmentID)
	if err != nil {
		return cleanup(err)
	}
	vnic, err := n.manager.api.GetVNIC(ctx, attachment, n.manager.VCNs())
	if err != nil {
		return cleanup(err)
	}

	addresses, err := n.manager.api.AssignPrivateIPAddresses(ctx, vnic.ID, toAllocate)
	if err != nil {
		return cleanup(err)
	}
	for _, address := range addresses {
		vnic.PrivateIPs = append(vnic.PrivateIPs, types.PrivateIP{Address: address})
	}

	n.mutex.Lock()
	n.vnics[vnic.ID] = *vnic.DeepCopy()
	n.mutex.Unlock()
	n.manager.UpdateVNIC(n.instanceID, vnic)
	scopedLog.Info("Attached OCI VNIC and allocated private IPs",
		"vnicID", vnic.ID,
		"attachmentID", attachmentID,
		"interfaceIndex", vnic.InterfaceIndex)
	return len(addresses), "", nil
}

func (n *Node) ResyncInterfacesAndIPs(_ context.Context, _ *slog.Logger) (ipamTypes.AllocationMap, stats.InterfaceStats, error) {
	instanceLimits, ok := n.getLimits()
	if !ok {
		return nil, stats.InterfaceStats{}, ipam.LimitsNotFound{}
	}

	available := ipamTypes.AllocationMap{}
	result := stats.InterfaceStats{NodeCapacity: instanceLimits.Adapters * instanceLimits.IPv4}
	vnics := map[string]types.VNIC{}
	n.manager.ForeachInstance(n.instanceID,
		func(_, _ string, revision ipamTypes.InterfaceRevision) error {
			vnic, ok := revision.Resource.(*types.VNIC)
			if !ok {
				return nil
			}
			vnics[vnic.ID] = *vnic.DeepCopy()
			secondaryCount := 0
			for _, privateIP := range vnic.PrivateIPs {
				if privateIP.IsPrimary || privateIP.Address == "" {
					continue
				}
				secondaryCount++
				available[privateIP.Address] = ipamTypes.AllocationIP{Resource: vnic.ID}
			}
			if secondaryCount < instanceLimits.IPv4 {
				result.RemainingAvailableInterfaceCount++
			}
			return nil
		})
	if len(vnics) == 0 {
		return nil, result, errors.New("unable to retrieve OCI VNICs for instance")
	}
	result.RemainingAvailableInterfaceCount += max(instanceLimits.Adapters-len(vnics), 0)

	n.mutex.Lock()
	n.vnics = vnics
	n.mutex.Unlock()
	return available, result, nil
}

func (n *Node) PrepareIPAllocation(scopedLog *slog.Logger) (*ipam.AllocationAction, error) {
	instanceLimits, ok := n.getLimits()
	if !ok {
		return nil, errors.New("unable to determine OCI VNIC limits")
	}
	action := &ipam.AllocationAction{}

	n.mutex.RLock()
	defer n.mutex.RUnlock()
	for id, vnic := range n.vnics {
		secondaryCount := 0
		for _, privateIP := range vnic.PrivateIPs {
			if !privateIP.IsPrimary {
				secondaryCount++
			}
		}
		availableOnVNIC := max(instanceLimits.IPv4-secondaryCount, 0)
		if availableOnVNIC == 0 {
			continue
		}
		action.IPv4.InterfaceCandidates++
		if action.InterfaceID != "" {
			continue
		}
		subnet := n.manager.GetSubnet(vnic.Subnet.ID)
		if subnet == nil || subnet.AvailableAddresses <= 0 {
			continue
		}
		action.InterfaceID = id
		action.PoolID = ipamTypes.PoolID(subnet.ID)
		action.IPv4.AvailableForAllocation = min(subnet.AvailableAddresses, availableOnVNIC)
		scopedLog.Debug("OCI VNIC has private IP capacity",
			"vnicID", id,
			"available", action.IPv4.AvailableForAllocation)
	}
	action.EmptyInterfaceSlots = max(instanceLimits.Adapters-len(n.vnics), 0)
	return action, nil
}

func (n *Node) AllocateIPs(ctx context.Context, allocation *ipam.AllocationAction) error {
	_, err := n.manager.api.AssignPrivateIPAddresses(
		ctx,
		allocation.InterfaceID,
		allocation.IPv4.AvailableForAllocation,
	)
	return err
}

func (n *Node) AllocateStaticIP(context.Context, ipamTypes.Tags) (string, error) {
	return "", errors.New("OCI static IP allocation is not supported")
}

func (n *Node) PrepareIPRelease(excessIPs int, scopedLog *slog.Logger) *ipam.ReleaseAction {
	release := &ipam.ReleaseAction{}
	n.mutex.RLock()
	defer n.mutex.RUnlock()
	for id, vnic := range n.vnics {
		var free []string
		statusVNIC, ok := n.k8sObj.Status.OCI.VNICs[id]
		if !ok {
			continue
		}
		for _, privateIP := range statusVNIC.PrivateIPs {
			if privateIP.IsPrimary || privateIP.Address == "" {
				continue
			}
			if _, used := n.k8sObj.Status.IPAM.Used[privateIP.Address]; !used {
				free = append(free, privateIP.Address)
			}
		}
		if len(free) == 0 {
			continue
		}
		toRelease := min(len(free), excessIPs)
		if len(release.IPsToRelease) >= toRelease {
			continue
		}
		release.InterfaceID = id
		release.PoolID = ipamTypes.PoolID(vnic.Subnet.ID)
		release.IPsToRelease = free[:toRelease]
		scopedLog.Debug("OCI VNIC has unused private IPs",
			"vnicID", id,
			"release", toRelease)
	}
	return release
}

func (n *Node) ReleaseIPPrefixes(context.Context, *ipam.ReleaseAction) error {
	return nil
}

func (n *Node) ReleaseIPs(ctx context.Context, release *ipam.ReleaseAction) error {
	return n.manager.api.UnassignPrivateIPAddresses(ctx, release.InterfaceID, release.IPsToRelease)
}

func (n *Node) GetMaximumAllocatableIPv4() int {
	instanceLimits, ok := n.getLimits()
	if !ok {
		return 0
	}
	return instanceLimits.Adapters * instanceLimits.IPv4
}

func (n *Node) GetMinimumAllocatableIPv4() int {
	return defaults.IPAMPreAllocation
}

func (n *Node) IsPrefixDelegated() bool {
	return false
}

func (n *Node) getLimits() (ipamTypes.Limits, bool) {
	n.mutex.RLock()
	defer n.mutex.RUnlock()
	if n.k8sObj == nil {
		return ipamTypes.Limits{}, false
	}
	return limits.Get(n.k8sObj.Spec.OCI.Shape)
}
