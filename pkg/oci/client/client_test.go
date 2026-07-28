// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package client

import (
	"context"
	"errors"
	"testing"

	ociCommon "github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/stretchr/testify/require"
)

type fakeCompute struct {
	detach func(core.DetachVnicRequest) error
}

func (f *fakeCompute) AttachVnic(context.Context, core.AttachVnicRequest) (core.AttachVnicResponse, error) {
	panic("unexpected AttachVnic call")
}
func (f *fakeCompute) DetachVnic(_ context.Context, request core.DetachVnicRequest) (core.DetachVnicResponse, error) {
	if f.detach == nil {
		panic("unexpected DetachVnic call")
	}
	return core.DetachVnicResponse{}, f.detach(request)
}
func (f *fakeCompute) GetVnicAttachment(context.Context, core.GetVnicAttachmentRequest) (core.GetVnicAttachmentResponse, error) {
	panic("unexpected GetVnicAttachment call")
}
func (f *fakeCompute) ListInstances(context.Context, core.ListInstancesRequest) (core.ListInstancesResponse, error) {
	panic("unexpected ListInstances call")
}
func (f *fakeCompute) ListShapes(context.Context, core.ListShapesRequest) (core.ListShapesResponse, error) {
	panic("unexpected ListShapes call")
}
func (f *fakeCompute) ListVnicAttachments(context.Context, core.ListVnicAttachmentsRequest) (core.ListVnicAttachmentsResponse, error) {
	panic("unexpected ListVnicAttachments call")
}

type fakeNetwork struct {
	createPrivateIP func(core.CreatePrivateIpRequest) (core.CreatePrivateIpResponse, error)
	deletePrivateIP func(core.DeletePrivateIpRequest) error
	listPrivateIPs  func(core.ListPrivateIpsRequest) (core.ListPrivateIpsResponse, error)
	listSubnets     func(core.ListSubnetsRequest) (core.ListSubnetsResponse, error)
	listVCNs        func(core.ListVcnsRequest) (core.ListVcnsResponse, error)
}

func (f *fakeNetwork) CreatePrivateIp(_ context.Context, request core.CreatePrivateIpRequest) (core.CreatePrivateIpResponse, error) {
	if f.createPrivateIP == nil {
		panic("unexpected CreatePrivateIp call")
	}
	return f.createPrivateIP(request)
}
func (f *fakeNetwork) DeletePrivateIp(_ context.Context, request core.DeletePrivateIpRequest) (core.DeletePrivateIpResponse, error) {
	if f.deletePrivateIP == nil {
		panic("unexpected DeletePrivateIp call")
	}
	return core.DeletePrivateIpResponse{}, f.deletePrivateIP(request)
}
func (f *fakeNetwork) GetSubnet(context.Context, core.GetSubnetRequest) (core.GetSubnetResponse, error) {
	panic("unexpected GetSubnet call")
}
func (f *fakeNetwork) GetVcn(context.Context, core.GetVcnRequest) (core.GetVcnResponse, error) {
	panic("unexpected GetVcn call")
}
func (f *fakeNetwork) GetVnic(context.Context, core.GetVnicRequest) (core.GetVnicResponse, error) {
	panic("unexpected GetVnic call")
}
func (f *fakeNetwork) ListPrivateIps(_ context.Context, request core.ListPrivateIpsRequest) (core.ListPrivateIpsResponse, error) {
	if f.listPrivateIPs == nil {
		panic("unexpected ListPrivateIps call")
	}
	return f.listPrivateIPs(request)
}
func (f *fakeNetwork) ListSubnets(_ context.Context, request core.ListSubnetsRequest) (core.ListSubnetsResponse, error) {
	if f.listSubnets == nil {
		panic("unexpected ListSubnets call")
	}
	return f.listSubnets(request)
}
func (f *fakeNetwork) ListVcns(_ context.Context, request core.ListVcnsRequest) (core.ListVcnsResponse, error) {
	if f.listVCNs == nil {
		panic("unexpected ListVcns call")
	}
	return f.listVCNs(request)
}

func TestGetVCNsFollowsPagination(t *testing.T) {
	var pages []string
	network := &fakeNetwork{
		listVCNs: func(request core.ListVcnsRequest) (core.ListVcnsResponse, error) {
			pages = append(pages, value(request.Page))
			if request.Page == nil {
				return core.ListVcnsResponse{
					Items: []core.Vcn{{
						Id:         ociCommon.String("vcn-1"),
						CidrBlocks: []string{"10.0.0.0/16", "10.1.0.0/16"},
					}},
					OpcNextPage: ociCommon.String("next"),
				}, nil
			}
			return core.ListVcnsResponse{
				Items: []core.Vcn{{
					Id:         ociCommon.String("vcn-2"),
					CidrBlocks: []string{"192.168.0.0/16"},
				}},
			}, nil
		},
	}

	vcns, err := newClient("compartment", &fakeCompute{}, network).GetVCNs(context.Background())

	require.NoError(t, err)
	require.Equal(t, []string{"", "next"}, pages)
	require.Len(t, vcns, 2)
	require.Equal(t, "10.0.0.0/16", vcns["vcn-1"].PrimaryCIDR)
	require.Equal(t, []string{"10.1.0.0/16"}, vcns["vcn-1"].CIDRs)
}

func TestGetSubnetsSubtractsAssignedAddresses(t *testing.T) {
	var privateIPPages []string
	network := &fakeNetwork{
		listSubnets: func(core.ListSubnetsRequest) (core.ListSubnetsResponse, error) {
			return core.ListSubnetsResponse{Items: []core.Subnet{{
				Id:             ociCommon.String("subnet-1"),
				VcnId:          ociCommon.String("vcn-1"),
				CidrBlock:      ociCommon.String("10.0.0.0/29"),
				LifecycleState: core.SubnetLifecycleStateAvailable,
			}}}, nil
		},
		listPrivateIPs: func(request core.ListPrivateIpsRequest) (core.ListPrivateIpsResponse, error) {
			require.Nil(t, request.VnicId)
			require.Equal(t, "subnet-1", value(request.SubnetId))
			privateIPPages = append(privateIPPages, value(request.Page))
			if request.Page == nil {
				return core.ListPrivateIpsResponse{
					Items:       []core.PrivateIp{{Id: ociCommon.String("ip-1")}},
					OpcNextPage: ociCommon.String("next"),
				}, nil
			}
			return core.ListPrivateIpsResponse{
				Items: []core.PrivateIp{{Id: ociCommon.String("ip-2")}},
			}, nil
		},
	}

	subnets, err := newClient("compartment", &fakeCompute{}, network).GetSubnets(context.Background())

	require.NoError(t, err)
	require.Equal(t, []string{"", "next"}, privateIPPages)
	require.Equal(t, 3, subnets["subnet-1"].AvailableAddresses)
}

func TestAssignPrivateIPAddressesHonorsCount(t *testing.T) {
	var createCalls int
	network := &fakeNetwork{
		createPrivateIP: func(request core.CreatePrivateIpRequest) (core.CreatePrivateIpResponse, error) {
			createCalls++
			require.Equal(t, "vnic-1", value(request.VnicId))
			require.NotEmpty(t, value(request.OpcRetryToken))
			return core.CreatePrivateIpResponse{PrivateIp: core.PrivateIp{
				Id:        ociCommon.String("ip-id-" + string(rune('0'+createCalls))),
				IpAddress: ociCommon.String("10.0.0." + string(rune('0'+createCalls))),
			}}, nil
		},
	}

	addresses, err := newClient("compartment", &fakeCompute{}, network).
		AssignPrivateIPAddresses(context.Background(), "vnic-1", 3)

	require.NoError(t, err)
	require.Equal(t, 3, createCalls)
	require.Equal(t, []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, addresses)
}

func TestAssignPrivateIPAddressesRollsBackPartialResult(t *testing.T) {
	var createCalls int
	var deleted []string
	network := &fakeNetwork{
		createPrivateIP: func(core.CreatePrivateIpRequest) (core.CreatePrivateIpResponse, error) {
			createCalls++
			if createCalls == 2 {
				return core.CreatePrivateIpResponse{}, errors.New("quota exceeded")
			}
			return core.CreatePrivateIpResponse{PrivateIp: core.PrivateIp{
				Id:        ociCommon.String("private-ip-id"),
				IpAddress: ociCommon.String("10.0.0.10"),
			}}, nil
		},
		deletePrivateIP: func(request core.DeletePrivateIpRequest) error {
			deleted = append(deleted, value(request.PrivateIpId))
			return nil
		},
	}

	addresses, err := newClient("compartment", &fakeCompute{}, network).
		AssignPrivateIPAddresses(context.Background(), "vnic-1", 2)

	require.ErrorContains(t, err, "quota exceeded")
	require.Nil(t, addresses)
	require.Equal(t, []string{"private-ip-id"}, deleted)
}

func TestUnassignPrivateIPAddressesDeletesByOCID(t *testing.T) {
	var deleted []string
	network := &fakeNetwork{
		listPrivateIPs: func(core.ListPrivateIpsRequest) (core.ListPrivateIpsResponse, error) {
			return core.ListPrivateIpsResponse{Items: []core.PrivateIp{
				{Id: ociCommon.String("primary-id"), IpAddress: ociCommon.String("10.0.0.1"), IsPrimary: ociCommon.Bool(true)},
				{Id: ociCommon.String("secondary-id"), IpAddress: ociCommon.String("10.0.0.2"), IsPrimary: ociCommon.Bool(false)},
			}}, nil
		},
		deletePrivateIP: func(request core.DeletePrivateIpRequest) error {
			deleted = append(deleted, value(request.PrivateIpId))
			return nil
		},
	}
	client := newClient("compartment", &fakeCompute{}, network)

	require.NoError(t, client.UnassignPrivateIPAddresses(context.Background(), "vnic-1", []string{"10.0.0.2"}))
	require.Equal(t, []string{"secondary-id"}, deleted)

	err := client.UnassignPrivateIPAddresses(context.Background(), "vnic-1", []string{"10.0.0.1"})
	require.ErrorContains(t, err, "refusing to delete primary")
	require.Equal(t, []string{"secondary-id"}, deleted)
}

func TestDetachVNICUsesAttachmentID(t *testing.T) {
	var detached string
	compute := &fakeCompute{
		detach: func(request core.DetachVnicRequest) error {
			detached = value(request.VnicAttachmentId)
			return nil
		},
	}

	err := newClient("compartment", compute, &fakeNetwork{}).
		DetachVNIC(context.Background(), "attachment-id")

	require.NoError(t, err)
	require.Equal(t, "attachment-id", detached)
}
