// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package types

import (
	"maps"
	"slices"

	ipamTypes "github.com/cilium/cilium/pkg/ipam/types"
)

const (
	VNICTypePrimary   = "Primary"
	VNICTypeSecondary = "Secondary"
)

// Spec is the OCI IPAM configuration stored in CiliumNode.Spec.
type Spec struct {
	Shape                 string            `json:"shape,omitempty"`
	CompartmentID         string            `json:"compartment-id,omitempty"`
	VCNID                 string            `json:"vcn-id,omitempty"`
	AvailabilityDomain    string            `json:"availability-domain,omitempty"`
	PrimaryVNICID         string            `json:"primary-vnic-id,omitempty"`
	PrimarySubnetID       string            `json:"primary-subnet-id,omitempty"`
	SubnetIDs             []string          `json:"subnet-ids,omitempty"`
	SubnetTags            map[string]string `json:"subnet-tags,omitempty"`
	NetworkSecurityGroups []string          `json:"network-security-groups,omitempty"`
}

func (in *Spec) DeepCopyInto(out *Spec) {
	*out = *in
	out.SubnetIDs = slices.Clone(in.SubnetIDs)
	out.SubnetTags = maps.Clone(in.SubnetTags)
	out.NetworkSecurityGroups = slices.Clone(in.NetworkSecurityGroups)
}

func (in *Spec) DeepEqual(other *Spec) bool {
	return other != nil &&
		in.Shape == other.Shape &&
		in.CompartmentID == other.CompartmentID &&
		in.VCNID == other.VCNID &&
		in.AvailabilityDomain == other.AvailabilityDomain &&
		in.PrimaryVNICID == other.PrimaryVNICID &&
		in.PrimarySubnetID == other.PrimarySubnetID &&
		slices.Equal(in.SubnetIDs, other.SubnetIDs) &&
		maps.Equal(in.SubnetTags, other.SubnetTags) &&
		slices.Equal(in.NetworkSecurityGroups, other.NetworkSecurityGroups)
}

// PrivateIP is an OCI private IP assigned to a VNIC.
type PrivateIP struct {
	ID        string `json:"id,omitempty"`
	Address   string `json:"address,omitempty"`
	IsPrimary bool   `json:"is-primary,omitempty"`
}

// Subnet contains routing information required by the OCI CNI path.
type Subnet struct {
	ID              string   `json:"id,omitempty"`
	CIDRs           []string `json:"cidrs,omitempty"`
	VirtualRouterIP string   `json:"virtual-router-ip,omitempty"`
}

// VCN contains all IPv4 CIDRs associated with an OCI virtual cloud network.
type VCN struct {
	ID    string   `json:"id,omitempty"`
	CIDRs []string `json:"cidrs,omitempty"`
}

// VNIC represents an OCI virtual network interface attached to an instance.
type VNIC struct {
	ID                 string      `json:"id,omitempty"`
	AttachmentID       string      `json:"attachment-id,omitempty"`
	DisplayName        string      `json:"display-name,omitempty"`
	PrimaryIP          string      `json:"primary-ip,omitempty"`
	MAC                string      `json:"mac,omitempty"`
	AvailabilityDomain string      `json:"availability-domain,omitempty"`
	VCN                VCN         `json:"vcn,omitempty"`
	Subnet             Subnet      `json:"subnet,omitempty"`
	PrivateIPs         []PrivateIP `json:"private-ips,omitempty"`
	SecurityGroups     []string    `json:"security-groups,omitempty"`
	IsPrimary          bool        `json:"is-primary,omitempty"`

	// InterfaceIndex is OCI's VLAN tag for the VNIC attachment. It is stable
	// for the lifetime of the attachment and provides a unique policy-routing
	// table selector on the node.
	InterfaceIndex int `json:"interface-index,omitempty"`
}

func (v *VNIC) DeepCopy() *VNIC {
	if v == nil {
		return nil
	}
	out := *v
	out.VCN.CIDRs = append([]string(nil), v.VCN.CIDRs...)
	out.Subnet.CIDRs = append([]string(nil), v.Subnet.CIDRs...)
	out.PrivateIPs = append([]PrivateIP(nil), v.PrivateIPs...)
	out.SecurityGroups = append([]string(nil), v.SecurityGroups...)
	return &out
}

func (v *VNIC) DeepCopyInterface() ipamTypes.Interface {
	return v.DeepCopy()
}

func (v *VNIC) InterfaceID() string {
	return v.ID
}

func (v *VNIC) ForeachAddress(instanceID string, fn ipamTypes.AddressIterator) error {
	for _, privateIP := range v.PrivateIPs {
		if privateIP.IsPrimary || privateIP.Address == "" {
			continue
		}
		if err := fn(instanceID, v.ID, privateIP.Address, "", privateIP); err != nil {
			return err
		}
	}
	return nil
}

// Status is the OCI-specific status stored in CiliumNode.Status.
type Status struct {
	VNICs map[string]VNIC `json:"vnics,omitempty"`
}

func (in *Status) DeepCopyInto(out *Status) {
	*out = *in
	if in.VNICs == nil {
		return
	}
	out.VNICs = make(map[string]VNIC, len(in.VNICs))
	for id, vnic := range in.VNICs {
		out.VNICs[id] = *vnic.DeepCopy()
	}
}

func (in *Status) DeepEqual(other *Status) bool {
	if other == nil || len(in.VNICs) != len(other.VNICs) {
		return false
	}
	for id, vnic := range in.VNICs {
		otherVNIC, ok := other.VNICs[id]
		if !ok || !vnic.deepEqual(&otherVNIC) {
			return false
		}
	}
	return true
}

func (v *VNIC) deepEqual(other *VNIC) bool {
	return other != nil &&
		v.ID == other.ID &&
		v.AttachmentID == other.AttachmentID &&
		v.DisplayName == other.DisplayName &&
		v.PrimaryIP == other.PrimaryIP &&
		v.MAC == other.MAC &&
		v.AvailabilityDomain == other.AvailabilityDomain &&
		v.VCN.ID == other.VCN.ID &&
		slices.Equal(v.VCN.CIDRs, other.VCN.CIDRs) &&
		v.Subnet.ID == other.Subnet.ID &&
		slices.Equal(v.Subnet.CIDRs, other.Subnet.CIDRs) &&
		v.Subnet.VirtualRouterIP == other.Subnet.VirtualRouterIP &&
		slices.Equal(v.PrivateIPs, other.PrivateIPs) &&
		slices.Equal(v.SecurityGroups, other.SecurityGroups) &&
		v.IsPrimary == other.IsPrimary &&
		v.InterfaceIndex == other.InterfaceIndex
}
