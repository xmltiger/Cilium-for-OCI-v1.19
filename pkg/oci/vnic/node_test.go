// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package vnic

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/cilium/cilium/pkg/ipam"
	ipamTypes "github.com/cilium/cilium/pkg/ipam/types"
	v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/cilium/cilium/pkg/oci/vnic/limits"
	"github.com/cilium/cilium/pkg/oci/vnic/types"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/stretchr/testify/require"
)

type testNodeActions struct{ id string }

func (n testNodeActions) InstanceID() string { return n.id }

type fakeAPI struct {
	attach       func(instanceID, subnetID string, nsgIDs []string, tags map[string]string) (string, error)
	wait         func(attachmentID string) (core.VnicAttachment, error)
	getVNIC      func(core.VnicAttachment) (*types.VNIC, error)
	detach       func(attachmentID string) error
	assign       func(vnicID string, count int) ([]string, error)
	unassign     func(vnicID string, addresses []string) error
	getVCNs      func() ipamTypes.VirtualNetworkMap
	getSubnets   func() ipamTypes.SubnetMap
	getInstances func() *ipamTypes.InstanceMap
	getInstance  func() *ipamTypes.Instance
}

func (f *fakeAPI) GetVCNs(context.Context) (ipamTypes.VirtualNetworkMap, error) {
	if f.getVCNs != nil {
		return f.getVCNs(), nil
	}
	return ipamTypes.VirtualNetworkMap{}, nil
}
func (f *fakeAPI) GetSubnets(context.Context) (ipamTypes.SubnetMap, error) {
	if f.getSubnets != nil {
		return f.getSubnets(), nil
	}
	return ipamTypes.SubnetMap{}, nil
}
func (f *fakeAPI) GetInstances(context.Context, ipamTypes.VirtualNetworkMap) (*ipamTypes.InstanceMap, error) {
	if f.getInstances != nil {
		return f.getInstances(), nil
	}
	return ipamTypes.NewInstanceMap(), nil
}
func (f *fakeAPI) GetInstance(context.Context, ipamTypes.VirtualNetworkMap, string) (*ipamTypes.Instance, error) {
	if f.getInstance != nil {
		return f.getInstance(), nil
	}
	return &ipamTypes.Instance{}, nil
}
func (f *fakeAPI) AttachVNIC(_ context.Context, instanceID, subnetID string, nsgIDs []string, tags map[string]string) (string, error) {
	return f.attach(instanceID, subnetID, nsgIDs, tags)
}
func (f *fakeAPI) WaitVNICAttached(_ context.Context, attachmentID string) (core.VnicAttachment, error) {
	return f.wait(attachmentID)
}
func (f *fakeAPI) GetVNIC(_ context.Context, attachment core.VnicAttachment, _ ipamTypes.VirtualNetworkMap) (*types.VNIC, error) {
	return f.getVNIC(attachment)
}
func (f *fakeAPI) DetachVNIC(_ context.Context, attachmentID string) error {
	return f.detach(attachmentID)
}
func (f *fakeAPI) AssignPrivateIPAddresses(_ context.Context, vnicID string, count int) ([]string, error) {
	return f.assign(vnicID, count)
}
func (f *fakeAPI) UnassignPrivateIPAddresses(_ context.Context, vnicID string, addresses []string) error {
	return f.unassign(vnicID, addresses)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNodeUsesPrimaryVNICCapacityAndUniqueRouteIndex(t *testing.T) {
	limits.Set("VM.Standard.Test", ipamTypes.Limits{Adapters: 2, IPv4: 32})
	api := &fakeAPI{}
	manager := NewInstancesManager(testLogger(), api)
	manager.subnets = ipamTypes.SubnetMap{
		"subnet": {ID: "subnet", VirtualNetworkID: "vcn", AvailableAddresses: 100},
	}
	vnic := &types.VNIC{
		ID:             "primary-vnic",
		Subnet:         types.Subnet{ID: "subnet", VirtualRouterIP: "10.0.0.1"},
		VCN:            types.VCN{ID: "vcn", CIDRs: []string{"10.0.0.0/16"}},
		MAC:            "00:00:17:01:02:03",
		IsPrimary:      true,
		InterfaceIndex: 0,
		PrivateIPs: []types.PrivateIP{
			{ID: "primary", Address: "10.0.0.2", IsPrimary: true},
			{ID: "pod-1", Address: "10.0.0.10"},
			{ID: "pod-2", Address: "10.0.0.11"},
		},
	}
	manager.UpdateVNIC("instance", vnic)
	resource := &v2.CiliumNode{
		Spec: v2.NodeSpec{InstanceID: "instance", OCI: types.Spec{Shape: "VM.Standard.Test", VCNID: "vcn"}},
		Status: v2.NodeStatus{
			OCI:  types.Status{VNICs: map[string]types.VNIC{vnic.ID: *vnic.DeepCopy()}},
			IPAM: ipamTypes.IPAMStatus{Used: ipamTypes.AllocationMap{"10.0.0.10": {}}},
		},
	}
	node := &Node{
		logger: testLogger(), node: testNodeActions{"instance"}, k8sObj: resource,
		manager: manager, instanceID: "instance", vnics: map[string]types.VNIC{},
	}

	available, stats, err := node.ResyncInterfacesAndIPs(context.Background(), testLogger())
	require.NoError(t, err)
	require.Len(t, available, 2)
	require.Equal(t, 64, stats.NodeCapacity)
	require.Equal(t, 2, stats.RemainingAvailableInterfaceCount)

	action, err := node.PrepareIPAllocation(testLogger())
	require.NoError(t, err)
	require.Equal(t, "primary-vnic", action.InterfaceID)
	require.Equal(t, 30, action.IPv4.AvailableForAllocation)
	require.Equal(t, 1, action.EmptyInterfaceSlots)

	release := node.PrepareIPRelease(1, testLogger())
	require.Equal(t, []string{"10.0.0.11"}, release.IPsToRelease)
	require.Equal(t, 64, node.GetMaximumAllocatableIPv4())
}

func TestCreateInterfaceCleansUpAfterAllocationFailure(t *testing.T) {
	limits.Set("VM.Standard.Test", ipamTypes.Limits{Adapters: 2, IPv4: 32})
	var detached string
	api := &fakeAPI{
		attach: func(instanceID, subnetID string, _ []string, tags map[string]string) (string, error) {
			require.Equal(t, "instance", instanceID)
			require.Equal(t, "subnet", subnetID)
			require.Equal(t, "true", tags["io.cilium/managed"])
			return "attachment", nil
		},
		wait: func(attachmentID string) (core.VnicAttachment, error) {
			return core.VnicAttachment{
				Id:             common.String(attachmentID),
				VnicId:         common.String("vnic"),
				LifecycleState: core.VnicAttachmentLifecycleStateAttached,
				VlanTag:        common.Int(201),
			}, nil
		},
		getVNIC: func(core.VnicAttachment) (*types.VNIC, error) {
			return &types.VNIC{ID: "vnic", Subnet: types.Subnet{ID: "subnet"}, InterfaceIndex: 201}, nil
		},
		assign: func(vnicID string, count int) ([]string, error) {
			require.Equal(t, "vnic", vnicID)
			require.Equal(t, 4, count)
			return nil, errors.New("allocation failed")
		},
		detach: func(attachmentID string) error {
			detached = attachmentID
			return nil
		},
	}
	manager := NewInstancesManager(testLogger(), api)
	manager.subnets = ipamTypes.SubnetMap{
		"subnet": {ID: "subnet", VirtualNetworkID: "vcn", AvailableAddresses: 100},
	}
	resource := &v2.CiliumNode{Spec: v2.NodeSpec{
		InstanceID: "instance",
		OCI:        types.Spec{Shape: "VM.Standard.Test", VCNID: "vcn"},
	}}
	node := &Node{
		logger: testLogger(), node: testNodeActions{"instance"}, k8sObj: resource,
		manager: manager, instanceID: "instance", vnics: map[string]types.VNIC{},
	}
	allocation := &ipam.AllocationAction{}
	allocation.IPv4.MaxIPsToAllocate = 4

	allocated, status, err := node.CreateInterface(context.Background(), allocation, testLogger())

	require.ErrorContains(t, err, "allocation failed")
	require.Zero(t, allocated)
	require.Equal(t, unableToAttachVNIC, status)
	require.Equal(t, "attachment", detached)
}
