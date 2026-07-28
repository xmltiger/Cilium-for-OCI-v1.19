.. only:: not (epub or latex or man)

   SPDX-License-Identifier: Apache-2.0
   Copyright Authors of Cilium

.. _ipam_oci:

OCI VNIC IPAM
==============

OCI VNIC IPAM assigns OCI secondary private IPv4 addresses directly to Pods.
Because those addresses are registered on the instance VNICs, other VCN
resources can route to Pod IPs without an overlay or a node-level NAT hop.

The Cilium operator discovers VCNs, subnets, compute shapes, instances, VNIC
attachments, and private IPs. It reconciles the desired pool in each
``CiliumNode``, assigns and releases secondary private IPs, and attaches
additional VNICs when existing VNICs are full. The agent uses the VNIC MAC,
subnet virtual-router address, VCN CIDRs, and OCI VLAN tag from the
``CiliumNode`` status to install per-VNIC policy routing.

.. warning::

   OCI VNIC IPAM currently supports IPv4 only. It is intended for new
   clusters; changing the IPAM mode of a running cluster is not supported.

Requirements
------------

* A supported Cilium host kernel and architecture.
* A regional or availability-domain-specific IPv4 subnet with enough private
  addresses for the requested Pod pool.
* The OCI VCN and worker instances must be reachable by the operator's OCI
  identity.
* Secondary VNICs must appear as Linux interfaces on the worker. Cilium
  matches them by MAC address and configures the required link and policy
  routes.
* Security lists and network security groups must allow the desired
  node-to-Pod, Pod-to-node, and VM-to-Pod traffic.

Authentication and IAM
----------------------

The default authentication method is OCI instance principal. Put the compute
instances that can run ``cilium-operator-oci`` in a narrowly scoped dynamic
group. The identity needs permission to:

* list compute shapes and instances;
* attach and detach VNICs on the worker instances;
* read VCNs, subnets, VNICs, and network security groups; and
* list, create, and delete private IPs.

Use the OCI IAM policy reference to translate these operations into
least-privilege statements for the compartments that contain the instances
and network. Avoid granting tenancy-wide ``manage all-resources``.

For development, ``oci.authMethod=config-file`` can use a mounted OCI SDK
configuration. Do not place SDK keys in Helm values or a ConfigMap.

Installation
------------

Build and publish both the regular Cilium image and the
``cilium-operator-oci`` image from this source tree. Then install the chart:

.. code-block:: shell-session

   helm install cilium ./install/kubernetes/cilium \
     --namespace kube-system \
     --set image.repository=REGISTRY/cilium \
     --set operator.image.repository=REGISTRY/cilium-operator \
     --set oci.enabled=true \
     --set oci.compartmentID=ocid1.compartment.oc1..example \
     --set oci.nodeSpec.vcnID=ocid1.vcn.oc1..example \
     --set ipv6.enabled=false

By default, all matching subnets in the VCN are candidates. Restrict
allocation with ``oci.nodeSpec.subnetIDs`` or free-form
``oci.nodeSpec.subnetTags`` values in ``key=value`` form. Use
``oci.nodeSpec.networkSecurityGroups`` to attach NSGs to Cilium-managed
secondary VNICs.

Set ``oci.releaseExcessIPs=true`` only after release and restart testing in the
target tenancy. The default keeps excess secondary addresses assigned to
reduce churn.

Validation
----------

Before production use, verify at least:

* allocation on the primary VNIC and after forcing a secondary VNIC;
* bidirectional VM-to-Pod and cross-node Pod traffic using the Pod address;
* agent and operator restart reconciliation without address duplication;
* Pod deletion and optional excess-address release;
* subnet exhaustion and OCI API throttling behavior; and
* node replacement and VNIC detachment cleanup.

Inspect ``CiliumNode.spec.ipam.pool`` and ``CiliumNode.status.oci.vnics`` while
testing. Every pool entry must reference an existing VNIC containing the same
secondary private IP, and every VNIC must have a non-negative interface index.
