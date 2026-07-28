// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

//go:build ipam_provider_oci

package cmd

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	operatorOption "github.com/cilium/cilium/operator/option"
	"github.com/cilium/cilium/pkg/option"
)

func init() {
	FlagsHooks = append(FlagsHooks, &ociFlagsHooks{})
}

type ociFlagsHooks struct{}

func (hook *ociFlagsHooks) RegisterProviderFlag(cmd *cobra.Command, vp *viper.Viper) {
	flags := cmd.Flags()

	flags.String(operatorOption.OCICompartmentID, "", "OCI compartment OCID containing the Kubernetes instances and VCNs")
	option.BindEnv(vp, operatorOption.OCICompartmentID)

	flags.String(operatorOption.OCIAuthMethod, "instance-principal", "OCI authentication method: instance-principal or config-file")
	option.BindEnv(vp, operatorOption.OCIAuthMethod)

	flags.String(operatorOption.OCIConfigFile, "", "OCI SDK config file path when --oci-auth-method=config-file")
	option.BindEnv(vp, operatorOption.OCIConfigFile)

	flags.String(operatorOption.OCIConfigProfile, "DEFAULT", "OCI SDK config profile when --oci-auth-method=config-file")
	option.BindEnv(vp, operatorOption.OCIConfigProfile)

	flags.Bool(operatorOption.OCIReleaseExcessIPs, false, "Enable release of unused OCI secondary private IP addresses")
	option.BindEnv(vp, operatorOption.OCIReleaseExcessIPs)

	vp.BindPFlags(flags)
}
