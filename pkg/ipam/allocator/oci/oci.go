// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package oci

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	operatorOption "github.com/cilium/cilium/operator/option"
	"github.com/cilium/cilium/pkg/api/helpers"
	apiMetrics "github.com/cilium/cilium/pkg/api/metrics"
	"github.com/cilium/cilium/pkg/ipam"
	"github.com/cilium/cilium/pkg/ipam/allocator"
	ipamMetrics "github.com/cilium/cilium/pkg/ipam/metrics"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/metrics"
	ociClient "github.com/cilium/cilium/pkg/oci/client"
	"github.com/cilium/cilium/pkg/oci/vnic"
	"github.com/cilium/cilium/pkg/oci/vnic/limits"

	ociCommon "github.com/oracle/oci-go-sdk/v65/common"
	ociAuth "github.com/oracle/oci-go-sdk/v65/common/auth"
	"github.com/oracle/oci-go-sdk/v65/core"
)

type Allocator struct {
	logger *slog.Logger
	client *ociClient.Client
}

func (a *Allocator) Init(ctx context.Context, logger *slog.Logger, reg *metrics.Registry) error {
	a.logger = logger.With(logfields.LogSubsys, "ipam-allocator-oci")
	if operatorOption.Config.OCICompartmentID == "" {
		return errors.New("--oci-compartment-id is required for OCI IPAM")
	}

	var provider ociCommon.ConfigurationProvider
	switch operatorOption.Config.OCIAuthMethod {
	case "", "instance-principal":
		var err error
		provider, err = ociAuth.InstancePrincipalConfigurationProvider()
		if err != nil {
			return fmt.Errorf("initialize OCI instance-principal authentication: %w", err)
		}
	case "config-file":
		if operatorOption.Config.OCIConfigFile == "" {
			return errors.New("--oci-config-file is required with --oci-auth-method=config-file")
		}
		provider = ociCommon.CustomProfileConfigProvider(
			operatorOption.Config.OCIConfigFile,
			operatorOption.Config.OCIConfigProfile,
		)
	default:
		return fmt.Errorf("unsupported OCI authentication method %q", operatorOption.Config.OCIAuthMethod)
	}

	compute, err := core.NewComputeClientWithConfigurationProvider(provider)
	if err != nil {
		return fmt.Errorf("create OCI compute client: %w", err)
	}
	network, err := core.NewVirtualNetworkClientWithConfigurationProvider(provider)
	if err != nil {
		return fmt.Errorf("create OCI virtual network client: %w", err)
	}
	var externalAPIMetrics helpers.MetricsAPI
	if operatorOption.Config.EnableMetrics {
		externalAPIMetrics = apiMetrics.NewPrometheusMetrics(metrics.Namespace, "oci", reg)
	} else {
		externalAPIMetrics = &apiMetrics.NoOpMetrics{}
	}
	a.client, err = ociClient.New(
		operatorOption.Config.OCICompartmentID,
		&compute,
		&network,
		externalAPIMetrics,
		operatorOption.Config.IPAMAPIQPSLimit,
		operatorOption.Config.IPAMAPIBurst,
	)
	if err != nil {
		return err
	}
	if err := limits.UpdateFromAPI(ctx, a.client); err != nil {
		return fmt.Errorf("discover OCI compute shape VNIC limits: %w", err)
	}
	return nil
}

func (a *Allocator) Start(ctx context.Context, getterUpdater ipam.CiliumNodeGetterUpdater, registry *metrics.Registry) (allocator.NodeEventHandler, error) {
	if a.client == nil {
		return nil, errors.New("OCI allocator was not initialized")
	}
	var providerMetrics ipam.MetricsAPI
	if operatorOption.Config.EnableMetrics {
		providerMetrics = ipamMetrics.NewPrometheusMetrics(metrics.Namespace, registry)
	} else {
		providerMetrics = &ipamMetrics.NoOpMetrics{}
	}

	instances := vnic.NewInstancesManager(a.logger, a.client)
	nodeManager, err := ipam.NewNodeManager(
		a.logger,
		instances,
		getterUpdater,
		providerMetrics,
		operatorOption.Config.ParallelAllocWorkers,
		operatorOption.Config.OCIReleaseExcessIPs,
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize OCI IPAM node manager: %w", err)
	}
	if err := nodeManager.Start(ctx); err != nil {
		return nil, err
	}
	return nodeManager, nil
}
