// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

//go:build ipam_provider_oci

package cmd

import (
	"github.com/cilium/cilium/pkg/ipam/allocator/oci"
	ipamOption "github.com/cilium/cilium/pkg/ipam/option"
)

func init() {
	allocatorProviders[ipamOption.IPAMOCI] = &oci.Allocator{}
}
