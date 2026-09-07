# 项目说明
github.com/xmltiger/Cilium-for-OCI的项目升级版本，修改了存在的bug，并发布安装包。

# 发布物
| 发布物 | 地址 |
| --- | --- |
| Cilium Agent | `ghcr.io/xmltiger/cilium:v1.19.6-oci.2` |
| OCI Operator | `ghcr.io/xmltiger/operator-oci:v1.19.6-oci.2` |
| Hubble Relay | `ghcr.io/xmltiger/hubble-relay:v1.19.6-oci.2` |
| Helm OCI Chart | `oci://ghcr.io/xmltiger/charts/cilium:1.19.6-oci.2` |

# 安装文档
在OCI上自建k8s集群，安装部署Cilium网络组件、以及OCI CCM、OCI CSI。
https://github.com/xmltiger/Cilium-for-OCI-v1.19/edit/1.19.6-oci.2/Documentation/installation/oci-vnic-ipam-installation-zh.md

# 安装相关组件版本
| 组件 | 版本/配置 |
| --- | --- |
| Kubernetes | `v1.34.9` |
| cri-tools | `v1.34.0` |
| kubernetes-cni | `v1.7.1` |
| OCI CCM/CSI | `v1.34.2` |
| Cilium OCI | `v1.19.6-oci.2` |
| Container Runtime | `containerd.io 2.2.6-1.el9`，配置 schema v3，systemd cgroup |
| Cilium IPAM | OCI VNIC IPAM |
| Kubernetes Service 转发 | Cilium eBPF KPR；kube-proxy 已移除 |
| Service CIDR | `10.96.0.0/12` |
| Pod CIDR | 不设置，由 Cilium OCI VNIC IPAM 管理 |
 
