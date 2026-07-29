# Cilium 1.19 OCI VNIC IPAM 构建、发布与安装指南

本文适用于 `xmltiger/Cilium-for-OCI` 仓库的 `oci-v1.19` 分支。该分支基于 Cilium
1.19.6，增加了 OCI VNIC IPAM、`cilium-operator-oci` 和对应 Helm 配置。

示例首发版本：

- Helm Chart：`1.19.6-oci.1`
- 容器镜像：`v1.19.6-oci.1`
- 平台：`linux/amd64`、`linux/arm64`

## 1. 发布物

正式发布包含：

| 发布物 | 地址 |
| --- | --- |
| Cilium Agent | `ghcr.io/xmltiger/cilium:v1.19.6-oci.1` |
| OCI Operator | `ghcr.io/xmltiger/operator-oci:v1.19.6-oci.1` |
| Hubble Relay | `ghcr.io/xmltiger/hubble-relay:v1.19.6-oci.1` |
| Helm OCI Chart | `oci://ghcr.io/xmltiger/charts/cilium:1.19.6-oci.1` |
| 离线 Chart 包 | `cilium-1.19.6-oci.1.tgz` |

Agent、OCI Operator 和 Hubble Relay 由本分支源码构建。Chart 仍复用上游固定
digest 的 Cilium Envoy、certgen、Hubble UI 等镜像；若集群不能访问 Quay.io，
需要额外镜像这些依赖并覆盖对应 Helm values。

## 2. 发布前检查

发布机或 GitHub Actions Runner 需要：

- Git；
- Docker 24+ 和 Docker Buildx；
- amd64/arm64 构建支持（本地通常通过 QEMU）；
- Helm 3.18；
- 对 `ghcr.io/xmltiger` 有 `write:packages` 权限。

确认分支和测试：

```bash
git switch oci-v1.19
git status --short
go test ./pkg/oci/...
go test -tags ipam_provider_oci \
  ./operator/cmd \
  ./pkg/ipam \
  ./pkg/ipam/allocator/oci \
  ./pkg/nodediscovery \
  ./plugins/cilium-cni/cmd
go build -tags ipam_provider_oci -o /tmp/cilium-operator-oci ./operator
```

工作区必须干净，并且 `VERSION` 应为 `1.19.6`。不要用相同 tag 覆盖已经发布的
镜像；发布修订时递增 `oci.N`。

## 3. 推荐发布方式：GitHub Actions

仓库中的 `.github/workflows/publish-oci-release.yaml` 会：

1. 校验版本；
2. 并行构建三个 `linux/amd64,linux/arm64` 镜像；
3. 推送镜像、SBOM 和构建 provenance 到 GHCR；
4. 将 Chart 默认镜像地址改成 `ghcr.io/xmltiger`；
5. 执行 `helm lint` 和 `helm template`；
6. 推送 Helm Chart，并保存 `.tgz`、示例 values 和本文档为 Actions artifact。

第一次发布：

```bash
git switch oci-v1.19
git tag -a v1.19.6-oci.1 -m "Cilium 1.19.6 OCI release 1"
git push origin oci-v1.19
git push origin v1.19.6-oci.1
```

也可以在 GitHub 仓库的 **Actions → Publish OCI Cilium release → Run
workflow** 中选择 `oci-v1.19`，输入 `1.19.6-oci.1`。

工作流使用仓库自带的 `GITHUB_TOKEN`，仓库的 Actions 设置必须允许该 token
写 Packages。首次推送后，在 GitHub Packages 中分别打开以下包，把
**Package visibility** 设为 **Public**，否则匿名 Kubernetes 节点无法拉取：

- `cilium`
- `operator-oci`
- `hubble-relay`
- `charts/cilium`

## 4. 本地发布（备用）

创建一个 Classic PAT，至少授予 `read:packages` 和 `write:packages`。不要把
token 写进脚本、Git 或 shell history。

```bash
export GHCR_USERNAME=xmltiger
read -rsp "GHCR token: " GHCR_TOKEN
export GHCR_TOKEN

contrib/oci-release/publish-local.sh 1.19.6-oci.1

unset GHCR_TOKEN
```

脚本会直接向 GHCR 推送镜像和 Chart。仅生成安装包而不推送：

```bash
contrib/oci-release/package-chart.sh \
  1.19.6-oci.1 \
  v1.19.6-oci.1 \
  ghcr.io/xmltiger \
  dist
```

输出：

```text
dist/cilium-1.19.6-oci.1.tgz
dist/cilium-1.19.6-oci.1-oci-values.yaml
```

## 5. 验证发布物

检查每个镜像的 manifest，输出中必须同时出现 `linux/amd64` 和
`linux/arm64`：

```bash
for image in cilium operator-oci hubble-relay; do
  docker buildx imagetools inspect \
    "ghcr.io/xmltiger/${image}:v1.19.6-oci.1"
done
```

拉取并渲染 Chart：

```bash
helm pull oci://ghcr.io/xmltiger/charts/cilium \
  --version 1.19.6-oci.1

helm template cilium oci://ghcr.io/xmltiger/charts/cilium \
  --version 1.19.6-oci.1 \
  --namespace kube-system \
  --set oci.enabled=true \
  --set oci.compartmentID=ocid1.compartment.oc1..example \
  --set oci.nodeSpec.vcnID=ocid1.vcn.oc1..example \
  --set ipv6.enabled=false |
  grep 'image:' | sort -u
```

应看到 `ghcr.io/xmltiger/cilium` 和
`ghcr.io/xmltiger/operator-oci`。Hubble Relay 只有在
`hubble.relay.enabled=true` 时才会渲染。

## 6. OCI 和 Kubernetes 前提

OCI VNIC IPAM 目前只支持 IPv4，并建议只用于新集群。不要把一个正在运行的
集群从其他 IPAM 模式直接切换到 OCI VNIC IPAM。

集群需要满足：

- Kubernetes 节点运行 Linux，内核符合 Cilium 1.19 要求；建议 5.10 或更高；
- 创建集群时没有安装其他 CNI，或已按原 CNI 的迁移流程处理；
- Pod 使用的 OCI 子网有足够的 IPv4 地址；
- Compute Shape 有足够的 VNIC 和每 VNIC 私有 IP 配额；
- 新附加的 VNIC 能作为 Linux 网卡出现在节点中；
- OCI Security List/NSG 允许节点、Pod、VCN 工作负载和控制面所需流量；
- 所有可能运行 `cilium-operator-oci` 的节点都能使用 OCI Instance Principal。

### 6.1 Dynamic Group

建议给 Operator 节点加专用实例标签，然后用标签或 compartment 创建
Dynamic Group。简单的 compartment 匹配示例：

```text
ALL {instance.compartment.id = 'ocid1.compartment.oc1..compute_compartment'}
```

如果所有 worker 都在该 compartment，这条规则会给所有 worker 相同权限。
生产环境应通过标签和 `operator.nodeSelector` 缩小范围。

### 6.2 IAM Policy

便于首次验证的 compartment 级策略如下。若网络资源在另一个 compartment，
把网络策略指向实际网络 compartment：

```text
Allow dynamic-group CiliumOperators to manage instance-family in compartment ComputeCompartment
Allow dynamic-group CiliumOperators to manage virtual-network-family in compartment NetworkCompartment
```

这组权限比较宽。验证通过后，应根据 OCI Audit 记录收敛到分支实际调用的
API：`ListShapes`、`ListInstances`、`ListVnicAttachments`、
`GetVnicAttachment`、`AttachVnic`、`DetachVnic`、`List/GetVcn`、
`List/GetSubnet`、`GetVnic`、`List/Create/DeletePrivateIp`。OCI 官方策略参考
指出，附加 VNIC 还需要管理实例、使用子网和 NSG；创建/删除私有 IP 需要使用
子网、VNIC 和 private IP。不要直接授予 tenancy 级
`manage all-resources`。

Instance Principal 和 Core Services Policy 官方说明：

- <https://docs.oracle.com/en-us/iaas/Content/Identity/Tasks/callingservicesfrominstances.htm>
- <https://docs.oracle.com/en-us/iaas/Content/Identity/Reference/corepolicyreference.htm>

## 7. 准备安装 values

从 Actions artifact 或源码复制示例：

```bash
cp contrib/oci-release/values-oci.example.yaml values-oci.yaml
```

如果直接使用已发布的 Helm OCI Chart，它的默认镜像已指向当前 release。
将示例文件中的：

- `__REGISTRY_NAMESPACE__` 替换为 `ghcr.io/xmltiger`；
- `__IMAGE_TAG__` 替换为 `v1.19.6-oci.1`；
- `oci.compartmentID` 替换为 Compute/Network 资源所在 compartment OCID；
- `oci.nodeSpec.vcnID` 替换为目标 VCN OCID。

建议显式列出 Pod 子网：

```yaml
oci:
  enabled: true
  compartmentID: "ocid1.compartment.oc1..real"
  authMethod: "instance-principal"
  releaseExcessIPs: false
  nodeSpec:
    vcnID: "ocid1.vcn.oc1..real"
    subnetIDs:
      - "ocid1.subnet.oc1.ap-tokyo-1.real"
    subnetTags: []
    networkSecurityGroups:
      - "ocid1.networksecuritygroup.oc1.ap-tokyo-1.real"

ipv4:
  enabled: true
ipv6:
  enabled: false
```

`subnetIDs` 为空时，Operator 会从 VCN 中选择匹配子网。生产环境建议明确
指定 subnet OCID。`releaseExcessIPs` 初次上线保持 `false`，完成 Pod
删除、Operator 重启、节点替换和 VNIC 回收测试后再考虑打开。

## 8. 安装

先确认当前 kubeconfig 指向目标集群：

```bash
kubectl config current-context
kubectl get nodes -o wide
```

如果 GHCR 包已经公开，直接安装：

```bash
helm install cilium oci://ghcr.io/xmltiger/charts/cilium \
  --version 1.19.6-oci.1 \
  --namespace kube-system \
  --values values-oci.yaml \
  --wait \
  --timeout 15m
```

如果 Chart 还未设为 public，先登录 Helm；若镜像也是 private，还要创建
image pull secret 并在 values 中设置 `imagePullSecrets`：

```bash
helm registry login ghcr.io --username xmltiger

kubectl -n kube-system create secret docker-registry ghcr-xmltiger \
  --docker-server=ghcr.io \
  --docker-username=xmltiger \
  --docker-password='<PAT>'
```

```yaml
imagePullSecrets:
  - name: ghcr-xmltiger
```

若使用下载的 `.tgz`：

```bash
helm install cilium ./cilium-1.19.6-oci.1.tgz \
  --namespace kube-system \
  --values values-oci.yaml \
  --wait \
  --timeout 15m
```

## 9. 安装验证

```bash
kubectl -n kube-system rollout status daemonset/cilium --timeout=10m
kubectl -n kube-system rollout status deployment/cilium-operator --timeout=10m
kubectl -n kube-system get pods -l k8s-app=cilium -o wide
kubectl -n kube-system get pods -l io.cilium/app=operator -o wide
kubectl get ciliumnodes -o wide
```

确认实际镜像：

```bash
kubectl -n kube-system get daemonset cilium \
  -o jsonpath='{.spec.template.spec.containers[*].image}{"\n"}'
kubectl -n kube-system get deployment cilium-operator \
  -o jsonpath='{.spec.template.spec.containers[*].image}{"\n"}'
```

如果已安装 Cilium CLI：

```bash
cilium status --wait
cilium connectivity test
```

OCI 特有检查：

```bash
kubectl get ciliumnodes -o yaml
kubectl -n kube-system logs deployment/cilium-operator \
  -c cilium-operator --since=20m |
  grep -Ei 'oci|vnic|private.?ip|error|warn'
```

重点确认：

- `CiliumNode.spec.ipam.pool` 已分配 Pod IP；
- `CiliumNode.status.oci.vnics` 中的 VNIC、MAC、interface index 和私有 IP
  与 OCI Console 一致；
- 同节点和跨节点 Pod 通信正常；
- VCN VM 可以按安全策略访问 Pod IP；
- 强制耗尽主 VNIC 地址后，辅助 VNIC 能成功附加并分配地址；
- Operator/Agent 重启不会造成地址重复或泄漏。

## 10. 升级与回滚

升级前保存当前配置：

```bash
helm -n kube-system get values cilium -o yaml > cilium-values-backup.yaml
helm -n kube-system history cilium
```

发布新修订后：

```bash
helm upgrade cilium oci://ghcr.io/xmltiger/charts/cilium \
  --version 1.19.6-oci.2 \
  --namespace kube-system \
  --values values-oci.yaml \
  --wait \
  --timeout 15m
```

回滚：

```bash
helm -n kube-system history cilium
helm -n kube-system rollback cilium <REVISION> --wait --timeout 15m
```

回滚前确认旧镜像仍在 GHCR。不要把 `helm uninstall cilium` 当作普通回滚手段；
删除正在使用的 CNI 可能让现有节点和 Pod 失去网络。

## 11. 常见故障

### ImagePullBackOff / 401

- 确认四个 GHCR 包为 public；
- 检查 tag 是否带 `v`；
- 私有包检查 `imagePullSecrets` 是否在 `kube-system`；
- 用 `docker manifest inspect` 或 `imagetools inspect` 检查节点架构是否存在。

### Operator 返回 OCI 401/403

- 确认 Pod 所在节点属于 Dynamic Group；
- 等待 IAM/Dynamic Group 规则传播；
- 在 OCI Audit 中按实例 principal OCID 查询拒绝的 API；
- 检查策略是在 Compute compartment、Network compartment 还是 tenancy 创建；
- 确认 `oci.compartmentID` 指向代码需要枚举资源的 compartment。

### Pod 一直 Pending，CiliumNode 没有可用 IP

- 检查子网剩余地址；
- 检查 Shape 的 VNIC 数和每 VNIC IPv4 数限制；
- 检查 `subnetIDs`/`subnetTags` 是否能匹配；
- 检查 Operator 日志中的 OCI 限流和配额错误；
- 检查辅助 VNIC 是否作为 Linux 网卡出现，MAC 是否与 OCI 返回值一致。

### Agent 正常但跨节点 Pod 不通

- 检查 Security List 和 NSG；
- 检查 VCN 路由与源/目标检查设置；
- 对比 `CiliumNode.status.oci.vnics`、`ip link`、`ip rule` 和 `ip route`；
- 确认 IPv6 已关闭，且没有残留的旧 CNI 配置。

## 12. 参考

- Cilium 1.19 Helm 安装：
  <https://docs.cilium.io/en/stable/installation/k8s-install-helm/>
- Cilium 系统要求：
  <https://docs.cilium.io/en/stable/operations/system_requirements/>
- OCI Instance Principal：
  <https://docs.oracle.com/en-us/iaas/Content/Identity/Tasks/callingservicesfrominstances.htm>
- OCI Core Services IAM Policy Reference：
  <https://docs.oracle.com/en-us/iaas/Content/Identity/Reference/corepolicyreference.htm>
