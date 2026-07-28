// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package client

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/cilium/cilium/pkg/api/helpers"
	ipamTypes "github.com/cilium/cilium/pkg/ipam/types"
	vnicTypes "github.com/cilium/cilium/pkg/oci/vnic/types"

	ociCommon "github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
)

const listPageSize = 100

const (
	operationAttachVNIC       = "AttachVnic"
	operationCreatePrivateIP  = "CreatePrivateIp"
	operationDeletePrivateIP  = "DeletePrivateIp"
	operationDetachVNIC       = "DetachVnic"
	operationGetSubnet        = "GetSubnet"
	operationGetVCN           = "GetVcn"
	operationGetVNIC          = "GetVnic"
	operationGetVNICAttach    = "GetVnicAttachment"
	operationListInstances    = "ListInstances"
	operationListPrivateIPs   = "ListPrivateIps"
	operationListShapes       = "ListShapes"
	operationListSubnets      = "ListSubnets"
	operationListVCNs         = "ListVcns"
	operationListVNICAttaches = "ListVnicAttachments"
)

type computeAPI interface {
	AttachVnic(context.Context, core.AttachVnicRequest) (core.AttachVnicResponse, error)
	DetachVnic(context.Context, core.DetachVnicRequest) (core.DetachVnicResponse, error)
	GetVnicAttachment(context.Context, core.GetVnicAttachmentRequest) (core.GetVnicAttachmentResponse, error)
	ListInstances(context.Context, core.ListInstancesRequest) (core.ListInstancesResponse, error)
	ListShapes(context.Context, core.ListShapesRequest) (core.ListShapesResponse, error)
	ListVnicAttachments(context.Context, core.ListVnicAttachmentsRequest) (core.ListVnicAttachmentsResponse, error)
}

type networkAPI interface {
	CreatePrivateIp(context.Context, core.CreatePrivateIpRequest) (core.CreatePrivateIpResponse, error)
	DeletePrivateIp(context.Context, core.DeletePrivateIpRequest) (core.DeletePrivateIpResponse, error)
	GetSubnet(context.Context, core.GetSubnetRequest) (core.GetSubnetResponse, error)
	GetVcn(context.Context, core.GetVcnRequest) (core.GetVcnResponse, error)
	GetVnic(context.Context, core.GetVnicRequest) (core.GetVnicResponse, error)
	ListPrivateIps(context.Context, core.ListPrivateIpsRequest) (core.ListPrivateIpsResponse, error)
	ListSubnets(context.Context, core.ListSubnetsRequest) (core.ListSubnetsResponse, error)
	ListVcns(context.Context, core.ListVcnsRequest) (core.ListVcnsResponse, error)
}

// Client is the OCI API surface used by the operator IPAM reconciler.
type Client struct {
	compartmentID string
	compute       computeAPI
	network       networkAPI
	limiter       *helpers.APILimiter
}

func New(
	compartmentID string,
	compute *core.ComputeClient,
	network *core.VirtualNetworkClient,
	metrics helpers.MetricsAPI,
	rateLimit float64,
	burst int,
) (*Client, error) {
	if strings.TrimSpace(compartmentID) == "" {
		return nil, errors.New("OCI compartment ID is required")
	}
	if compute == nil || network == nil {
		return nil, errors.New("OCI compute and virtual network clients are required")
	}
	if metrics == nil || rateLimit <= 0 || burst <= 0 {
		return nil, errors.New("OCI API rate-limit metrics, QPS, and burst are required")
	}
	client := newClient(compartmentID, compute, network)
	client.limiter = helpers.NewAPILimiter(metrics, rateLimit, burst)
	return client, nil
}

func newClient(compartmentID string, compute computeAPI, network networkAPI) *Client {
	return &Client{
		compartmentID: strings.TrimSpace(compartmentID),
		compute:       compute,
		network:       network,
	}
}

func (c *Client) limit(ctx context.Context, operation string) {
	if c.limiter != nil {
		c.limiter.Limit(ctx, operation)
	}
}

func value(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func intValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func retryMetadata() ociCommon.RequestMetadata {
	policy := ociCommon.DefaultRetryPolicy()
	return ociCommon.RequestMetadata{RetryPolicy: &policy}
}

// GetInstanceTypeLimits returns the VNIC attachment limit advertised for
// every shape available in the compartment. OCI permits 32 secondary private
// IPv4 addresses per VNIC.
func (c *Client) GetInstanceTypeLimits(ctx context.Context) (map[string]ipamTypes.Limits, error) {
	result := map[string]ipamTypes.Limits{}
	var page *string
	for {
		c.limit(ctx, operationListShapes)
		response, err := c.compute.ListShapes(ctx, core.ListShapesRequest{
			CompartmentId:   ociCommon.String(c.compartmentID),
			Limit:           ociCommon.Int(listPageSize),
			Page:            page,
			RequestMetadata: retryMetadata(),
		})
		if err != nil {
			return nil, fmt.Errorf("list OCI compute shapes: %w", err)
		}
		for _, shape := range response.Items {
			name := value(shape.Shape)
			adapters := intValue(shape.MaxVnicAttachments)
			if name == "" || adapters <= 0 {
				continue
			}
			result[name] = ipamTypes.Limits{Adapters: adapters, IPv4: 32}
		}
		if value(response.OpcNextPage) == "" {
			return result, nil
		}
		page = response.OpcNextPage
	}
}

// GetVCN resolves one VCN directly by OCID.
func (c *Client) GetVCN(ctx context.Context, vcnID string) (*ipamTypes.VirtualNetwork, error) {
	if strings.TrimSpace(vcnID) == "" {
		return nil, errors.New("VCN ID is required")
	}
	c.limit(ctx, operationGetVCN)
	response, err := c.network.GetVcn(ctx, core.GetVcnRequest{
		VcnId:           ociCommon.String(vcnID),
		RequestMetadata: retryMetadata(),
	})
	if err != nil {
		return nil, fmt.Errorf("get OCI VCN %q: %w", vcnID, err)
	}
	return virtualNetworkFromSDK(response.Vcn)
}

func virtualNetworkFromSDK(vcn core.Vcn) (*ipamTypes.VirtualNetwork, error) {
	id := value(vcn.Id)
	if id == "" {
		return nil, errors.New("OCI VCN response is missing ID")
	}
	cidrs := slices.Clone(vcn.CidrBlocks)
	if len(cidrs) == 0 {
		return nil, fmt.Errorf("OCI VCN %q has no IPv4 CIDR blocks", id)
	}
	return &ipamTypes.VirtualNetwork{
		ID:          id,
		PrimaryCIDR: cidrs[0],
		CIDRs:       slices.Clone(cidrs[1:]),
		IPv6CIDRs:   slices.Clone(vcn.Ipv6CidrBlocks),
	}, nil
}

// GetVCNs returns every VCN in the configured compartment, following all OCI
// pagination tokens.
func (c *Client) GetVCNs(ctx context.Context) (ipamTypes.VirtualNetworkMap, error) {
	result := ipamTypes.VirtualNetworkMap{}
	var page *string
	for {
		c.limit(ctx, operationListVCNs)
		response, err := c.network.ListVcns(ctx, core.ListVcnsRequest{
			CompartmentId:   ociCommon.String(c.compartmentID),
			Limit:           ociCommon.Int(listPageSize),
			Page:            page,
			RequestMetadata: retryMetadata(),
		})
		if err != nil {
			return nil, fmt.Errorf("list OCI VCNs: %w", err)
		}
		for _, item := range response.Items {
			vcn, err := virtualNetworkFromSDK(item)
			if err != nil {
				return nil, err
			}
			result[vcn.ID] = vcn
		}
		if value(response.OpcNextPage) == "" {
			return result, nil
		}
		page = response.OpcNextPage
	}
}

func subnetFromSDK(subnet core.Subnet) (*ipamTypes.Subnet, error) {
	id := value(subnet.Id)
	vcnID := value(subnet.VcnId)
	if id == "" || vcnID == "" {
		return nil, errors.New("OCI subnet response is missing subnet or VCN ID")
	}

	cidrText := value(subnet.CidrBlock)
	if cidrText == "" && len(subnet.Ipv4CidrBlocks) > 0 {
		cidrText = subnet.Ipv4CidrBlocks[0]
	}
	cidr, err := netip.ParsePrefix(cidrText)
	if err != nil {
		return nil, fmt.Errorf("parse OCI subnet %q CIDR %q: %w", id, cidrText, err)
	}
	if !cidr.Addr().Is4() {
		return nil, fmt.Errorf("OCI subnet %q CIDR %q is not IPv4", id, cidrText)
	}

	// OCI reserves the first two and last IPv4 address in each subnet. This is
	// only a coarse quota; create calls remain authoritative under contention.
	addresses := int(uint64(1) << uint(32-cidr.Bits()))
	addresses = max(addresses-3, 0)

	return &ipamTypes.Subnet{
		ID:                 id,
		Name:               value(subnet.DisplayName),
		CIDR:               cidr,
		AvailabilityZone:   value(subnet.AvailabilityDomain),
		VirtualNetworkID:   vcnID,
		AvailableAddresses: addresses,
		Tags:               ipamTypes.Tags(subnet.FreeformTags),
	}, nil
}

// GetSubnets returns every available subnet in the configured compartment.
func (c *Client) GetSubnets(ctx context.Context) (ipamTypes.SubnetMap, error) {
	result := ipamTypes.SubnetMap{}
	var page *string
	for {
		c.limit(ctx, operationListSubnets)
		response, err := c.network.ListSubnets(ctx, core.ListSubnetsRequest{
			CompartmentId:   ociCommon.String(c.compartmentID),
			Limit:           ociCommon.Int(listPageSize),
			Page:            page,
			RequestMetadata: retryMetadata(),
		})
		if err != nil {
			return nil, fmt.Errorf("list OCI subnets: %w", err)
		}
		for _, item := range response.Items {
			if item.LifecycleState != "" && item.LifecycleState != core.SubnetLifecycleStateAvailable {
				continue
			}
			subnet, err := subnetFromSDK(item)
			if err != nil {
				return nil, err
			}
			privateIPs, err := c.listPrivateIPs(ctx, "", subnet.ID)
			if err != nil {
				return nil, err
			}
			subnet.AvailableAddresses = max(subnet.AvailableAddresses-len(privateIPs), 0)
			result[subnet.ID] = subnet
		}
		if value(response.OpcNextPage) == "" {
			return result, nil
		}
		page = response.OpcNextPage
	}
}

func (c *Client) listPrivateIPs(ctx context.Context, vnicID, subnetID string) ([]core.PrivateIp, error) {
	var result []core.PrivateIp
	var page *string
	for {
		request := core.ListPrivateIpsRequest{
			Limit:           ociCommon.Int(listPageSize),
			Page:            page,
			RequestMetadata: retryMetadata(),
		}
		if vnicID != "" {
			request.VnicId = ociCommon.String(vnicID)
		}
		if subnetID != "" {
			request.SubnetId = ociCommon.String(subnetID)
		}
		c.limit(ctx, operationListPrivateIPs)
		response, err := c.network.ListPrivateIps(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("list OCI private IPs for VNIC %q and subnet %q: %w", vnicID, subnetID, err)
		}
		result = append(result, response.Items...)
		if value(response.OpcNextPage) == "" {
			return result, nil
		}
		page = response.OpcNextPage
	}
}

func (c *Client) vnicFromAttachment(ctx context.Context, attachment core.VnicAttachment, vcns ipamTypes.VirtualNetworkMap) (*vnicTypes.VNIC, error) {
	attachmentID, vnicID := value(attachment.Id), value(attachment.VnicId)
	if attachmentID == "" || vnicID == "" {
		return nil, errors.New("OCI VNIC attachment is missing attachment or VNIC ID")
	}

	c.limit(ctx, operationGetVNIC)
	vnicResponse, err := c.network.GetVnic(ctx, core.GetVnicRequest{
		VnicId:          ociCommon.String(vnicID),
		RequestMetadata: retryMetadata(),
	})
	if err != nil {
		return nil, fmt.Errorf("get OCI VNIC %q: %w", vnicID, err)
	}
	vnic := vnicResponse.Vnic
	subnetID := value(vnic.SubnetId)
	if subnetID == "" {
		return nil, fmt.Errorf("OCI VNIC %q is not attached to a subnet", vnicID)
	}

	c.limit(ctx, operationGetSubnet)
	subnetResponse, err := c.network.GetSubnet(ctx, core.GetSubnetRequest{
		SubnetId:        ociCommon.String(subnetID),
		RequestMetadata: retryMetadata(),
	})
	if err != nil {
		return nil, fmt.Errorf("get subnet %q for OCI VNIC %q: %w", subnetID, vnicID, err)
	}
	subnet := subnetResponse.Subnet
	vcnID := value(subnet.VcnId)
	vcn := vcns[vcnID]
	if vcn == nil {
		resolved, err := c.GetVCN(ctx, vcnID)
		if err != nil {
			return nil, err
		}
		vcn = resolved
	}

	privateIPs, err := c.listPrivateIPs(ctx, vnicID, "")
	if err != nil {
		return nil, err
	}
	addresses := make([]vnicTypes.PrivateIP, 0, len(privateIPs))
	for _, privateIP := range privateIPs {
		id, address := value(privateIP.Id), value(privateIP.IpAddress)
		if id == "" || address == "" {
			continue
		}
		addresses = append(addresses, vnicTypes.PrivateIP{
			ID:        id,
			Address:   address,
			IsPrimary: privateIP.IsPrimary != nil && *privateIP.IsPrimary,
		})
	}

	subnetCIDRs := slices.Clone(subnet.Ipv4CidrBlocks)
	if len(subnetCIDRs) == 0 && value(subnet.CidrBlock) != "" {
		subnetCIDRs = []string{value(subnet.CidrBlock)}
	}
	vcnCIDRs := append([]string{vcn.PrimaryCIDR}, vcn.CIDRs...)
	interfaceIndex := -1
	if attachment.VlanTag != nil {
		interfaceIndex = *attachment.VlanTag
	}
	return &vnicTypes.VNIC{
		ID:                 vnicID,
		AttachmentID:       attachmentID,
		DisplayName:        value(vnic.DisplayName),
		PrimaryIP:          value(vnic.PrivateIp),
		MAC:                value(vnic.MacAddress),
		AvailabilityDomain: value(vnic.AvailabilityDomain),
		VCN:                vnicTypes.VCN{ID: vcnID, CIDRs: vcnCIDRs},
		Subnet: vnicTypes.Subnet{
			ID:              subnetID,
			CIDRs:           subnetCIDRs,
			VirtualRouterIP: value(subnet.VirtualRouterIp),
		},
		PrivateIPs:     addresses,
		SecurityGroups: slices.Clone(vnic.NsgIds),
		IsPrimary:      vnic.IsPrimary != nil && *vnic.IsPrimary,
		InterfaceIndex: interfaceIndex,
	}, nil
}

// GetVNIC resolves a completed attachment into the Cilium OCI VNIC model.
func (c *Client) GetVNIC(ctx context.Context, attachment core.VnicAttachment, vcns ipamTypes.VirtualNetworkMap) (*vnicTypes.VNIC, error) {
	return c.vnicFromAttachment(ctx, attachment, vcns)
}

func (c *Client) listVNICAttachments(ctx context.Context, instanceID string) ([]core.VnicAttachment, error) {
	var result []core.VnicAttachment
	var page *string
	for {
		c.limit(ctx, operationListVNICAttaches)
		response, err := c.compute.ListVnicAttachments(ctx, core.ListVnicAttachmentsRequest{
			CompartmentId:   ociCommon.String(c.compartmentID),
			InstanceId:      ociCommon.String(instanceID),
			Limit:           ociCommon.Int(listPageSize),
			Page:            page,
			RequestMetadata: retryMetadata(),
		})
		if err != nil {
			return nil, fmt.Errorf("list VNIC attachments for OCI instance %q: %w", instanceID, err)
		}
		result = append(result, response.Items...)
		if value(response.OpcNextPage) == "" {
			return result, nil
		}
		page = response.OpcNextPage
	}
}

// GetInstance returns one instance's attached VNICs.
func (c *Client) GetInstance(ctx context.Context, vcns ipamTypes.VirtualNetworkMap, instanceID string) (*ipamTypes.Instance, error) {
	attachments, err := c.listVNICAttachments(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	instance := &ipamTypes.Instance{Interfaces: map[string]ipamTypes.InterfaceRevision{}}
	for _, attachment := range attachments {
		if attachment.LifecycleState != core.VnicAttachmentLifecycleStateAttached {
			continue
		}
		vnic, err := c.vnicFromAttachment(ctx, attachment, vcns)
		if err != nil {
			return nil, err
		}
		instance.Interfaces[vnic.ID] = ipamTypes.InterfaceRevision{Resource: vnic}
	}
	return instance, nil
}

// GetInstances returns all running instances and their VNICs, following
// pagination at both the instance and attachment layers.
func (c *Client) GetInstances(ctx context.Context, vcns ipamTypes.VirtualNetworkMap) (*ipamTypes.InstanceMap, error) {
	result := ipamTypes.NewInstanceMap()
	var page *string
	for {
		c.limit(ctx, operationListInstances)
		response, err := c.compute.ListInstances(ctx, core.ListInstancesRequest{
			CompartmentId:   ociCommon.String(c.compartmentID),
			LifecycleState:  core.InstanceLifecycleStateRunning,
			Limit:           ociCommon.Int(listPageSize),
			Page:            page,
			RequestMetadata: retryMetadata(),
		})
		if err != nil {
			return nil, fmt.Errorf("list OCI instances: %w", err)
		}
		for _, item := range response.Items {
			instanceID := value(item.Id)
			if instanceID == "" {
				continue
			}
			instance, err := c.GetInstance(ctx, vcns, instanceID)
			if err != nil {
				return nil, err
			}
			result.UpdateInstance(instanceID, instance)
		}
		if value(response.OpcNextPage) == "" {
			return result, nil
		}
		page = response.OpcNextPage
	}
}

// AttachVNIC creates and attaches a private secondary VNIC.
func (c *Client) AttachVNIC(ctx context.Context, instanceID, subnetID string, nsgIDs []string, tags map[string]string) (string, error) {
	if instanceID == "" || subnetID == "" {
		return "", errors.New("instance and subnet IDs are required to attach an OCI VNIC")
	}
	assignPublicIP := false
	c.limit(ctx, operationAttachVNIC)
	response, err := c.compute.AttachVnic(ctx, core.AttachVnicRequest{
		AttachVnicDetails: core.AttachVnicDetails{
			InstanceId: ociCommon.String(instanceID),
			CreateVnicDetails: &core.CreateVnicDetails{
				SubnetId:       ociCommon.String(subnetID),
				AssignPublicIp: &assignPublicIP,
				NsgIds:         slices.Clone(nsgIDs),
				FreeformTags:   tags,
			},
		},
		OpcRetryToken:   ociCommon.String(ociCommon.RetryToken()),
		RequestMetadata: retryMetadata(),
	})
	if err != nil {
		return "", fmt.Errorf("attach OCI VNIC to instance %q: %w", instanceID, err)
	}
	attachmentID := value(response.Id)
	if attachmentID == "" {
		return "", errors.New("OCI AttachVnic response is missing attachment ID")
	}
	return attachmentID, nil
}

// WaitVNICAttached waits until OCI reports the attachment and VLAN tag needed
// for policy routing.
func (c *Client) WaitVNICAttached(ctx context.Context, attachmentID string) (core.VnicAttachment, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		c.limit(waitCtx, operationGetVNICAttach)
		response, err := c.compute.GetVnicAttachment(waitCtx, core.GetVnicAttachmentRequest{
			VnicAttachmentId: ociCommon.String(attachmentID),
			RequestMetadata:  retryMetadata(),
		})
		if err != nil {
			return core.VnicAttachment{}, fmt.Errorf("get OCI VNIC attachment %q: %w", attachmentID, err)
		}
		attachment := response.VnicAttachment
		switch attachment.LifecycleState {
		case core.VnicAttachmentLifecycleStateAttached:
			if value(attachment.VnicId) == "" {
				return core.VnicAttachment{}, fmt.Errorf("attached OCI VNIC attachment %q has no VNIC ID", attachmentID)
			}
			return attachment, nil
		case core.VnicAttachmentLifecycleStateDetached:
			return core.VnicAttachment{}, fmt.Errorf("OCI VNIC attachment %q became detached", attachmentID)
		}

		select {
		case <-waitCtx.Done():
			return core.VnicAttachment{}, fmt.Errorf("wait for OCI VNIC attachment %q: %w", attachmentID, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// DetachVNIC performs the actual OCI delete operation for a VNIC attachment.
func (c *Client) DetachVNIC(ctx context.Context, attachmentID string) error {
	if attachmentID == "" {
		return errors.New("OCI VNIC attachment ID is required")
	}
	c.limit(ctx, operationDetachVNIC)
	_, err := c.compute.DetachVnic(ctx, core.DetachVnicRequest{
		VnicAttachmentId: ociCommon.String(attachmentID),
		RequestMetadata:  retryMetadata(),
	})
	if err != nil {
		return fmt.Errorf("detach OCI VNIC attachment %q: %w", attachmentID, err)
	}
	return nil
}

// AssignPrivateIPAddresses creates exactly count secondary private IPs. Any
// partial allocation is rolled back if a later create fails.
func (c *Client) AssignPrivateIPAddresses(ctx context.Context, vnicID string, count int) ([]string, error) {
	if vnicID == "" {
		return nil, errors.New("OCI VNIC ID is required")
	}
	if count < 0 {
		return nil, errors.New("private IP allocation count cannot be negative")
	}
	addresses := make([]string, 0, count)
	ids := make([]string, 0, count)
	for range count {
		c.limit(ctx, operationCreatePrivateIP)
		response, err := c.network.CreatePrivateIp(ctx, core.CreatePrivateIpRequest{
			CreatePrivateIpDetails: core.CreatePrivateIpDetails{VnicId: ociCommon.String(vnicID)},
			OpcRetryToken:          ociCommon.String(ociCommon.RetryToken()),
			RequestMetadata:        retryMetadata(),
		})
		if err != nil {
			rollbackErr := c.deletePrivateIPIDs(ctx, ids)
			return nil, errors.Join(fmt.Errorf("create private IP on OCI VNIC %q: %w", vnicID, err), rollbackErr)
		}
		id, address := value(response.Id), value(response.IpAddress)
		if id == "" || address == "" {
			rollbackErr := c.deletePrivateIPIDs(ctx, ids)
			return nil, errors.Join(errors.New("OCI CreatePrivateIp response is missing ID or address"), rollbackErr)
		}
		ids = append(ids, id)
		addresses = append(addresses, address)
	}
	return addresses, nil
}

func (c *Client) deletePrivateIPIDs(ctx context.Context, ids []string) error {
	var result error
	for _, id := range ids {
		c.limit(ctx, operationDeletePrivateIP)
		if _, err := c.network.DeletePrivateIp(ctx, core.DeletePrivateIpRequest{
			PrivateIpId:     ociCommon.String(id),
			RequestMetadata: retryMetadata(),
		}); err != nil {
			result = errors.Join(result, fmt.Errorf("delete OCI private IP %q: %w", id, err))
		}
	}
	return result
}

// UnassignPrivateIPAddresses resolves requested addresses to their OCIDs,
// rejects primary addresses, and deletes each secondary address.
func (c *Client) UnassignPrivateIPAddresses(ctx context.Context, vnicID string, addresses []string) error {
	if len(addresses) == 0 {
		return nil
	}
	privateIPs, err := c.listPrivateIPs(ctx, vnicID, "")
	if err != nil {
		return err
	}

	byAddress := make(map[string]core.PrivateIp, len(privateIPs))
	for _, privateIP := range privateIPs {
		if address := value(privateIP.IpAddress); address != "" {
			byAddress[address] = privateIP
		}
	}

	ids := make([]string, 0, len(addresses))
	for _, address := range addresses {
		privateIP, ok := byAddress[address]
		if !ok {
			return fmt.Errorf("OCI private IP %q is not assigned to VNIC %q", address, vnicID)
		}
		if privateIP.IsPrimary != nil && *privateIP.IsPrimary {
			return fmt.Errorf("refusing to delete primary OCI private IP %q", address)
		}
		id := value(privateIP.Id)
		if id == "" {
			return fmt.Errorf("OCI private IP %q has no OCID", address)
		}
		ids = append(ids, id)
	}
	return c.deletePrivateIPIDs(ctx, ids)
}
