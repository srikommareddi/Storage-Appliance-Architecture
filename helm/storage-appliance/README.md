# Storage Appliance Helm Chart

This Helm chart deploys the Storage Appliance Architecture on Kubernetes, providing S3-compatible object storage, distributed filesystem, and NVMe-oF capabilities.

## Prerequisites

- Kubernetes 1.20+
- Helm 3.0+
- PersistentVolume provisioner support in the underlying infrastructure
- (Optional) SPDK-enabled nodes for CGO mode

## Installation

### Quick Start

```bash
helm install storage-appliance ./helm/storage-appliance
```

### Custom Installation

```bash
helm install storage-appliance ./helm/storage-appliance \
  --set replicaCount=5 \
  --set unity.deduplication.enabled=true \
  --set powerscale.erasureCoding.dataStripes=4 \
  --set powerscale.erasureCoding.parityStripes=2
```

### Install with Values File

```bash
helm install storage-appliance ./helm/storage-appliance \
  -f custom-values.yaml
```

## Configuration

### Key Configuration Parameters

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of storage appliance replicas | `3` |
| `unity.enabled` | Enable S3 object storage | `true` |
| `unity.deduplication.enabled` | Enable Data Domain deduplication | `true` |
| `unity.deduplication.mode` | Dedup mode (inline/post-process) | `inline` |
| `unity.compression.enabled` | Enable compression | `true` |
| `unity.compression.algorithm` | Compression algorithm (gzip/lz4/zstd) | `zstd` |
| `powerscale.enabled` | Enable distributed filesystem | `true` |
| `powerscale.erasureCoding.dataStripes` | Number of data stripes (N) | `4` |
| `powerscale.erasureCoding.parityStripes` | Number of parity stripes (M) | `2` |
| `nvmeof.enabled` | Enable NVMe-oF | `true` |
| `nvmeof.transport` | Transport protocol (tcp/rdma) | `tcp` |
| `vplex.enabled` | Enable metro clustering | `false` |
| `persistence.size` | Storage size per pod | `500Gi` |
| `resources.limits.memory` | Memory limit | `8Gi` |
| `resources.limits.cpu` | CPU limit | `4` |

### Unity (S3 Object Storage) Configuration

```yaml
unity:
  enabled: true
  deduplication:
    enabled: true
    mode: "inline"  # inline or post-process
  compression:
    enabled: true
    algorithm: "zstd"  # gzip, lz4, or zstd
  storage:
    capacity: "100Gi"
    class: "fast-ssd"
```

### PowerScale (Distributed Filesystem) Configuration

```yaml
powerscale:
  enabled: true
  cluster:
    name: "prod-cluster"
    nodes: 5
  erasureCoding:
    dataStripes: 4  # N
    parityStripes: 2  # M (N+M protection)
  storage:
    capacity: "1Ti"
    class: "standard"
```

### VPLEX Metro Clustering Configuration

```yaml
vplex:
  enabled: true
  sites:
    - name: "DC1"
      location: "us-west"
    - name: "DC2"
      location: "us-east"
  maxLatency: "5ms"
  witnessEnabled: true
```

### NVMe-oF Configuration

```yaml
nvmeof:
  enabled: true
  transport: "tcp"  # tcp or rdma
  subsystems:
    - nqn: "nqn.2024-01.com.storage:subsys1"
      namespaces:
        - nsid: 1
          size: "100Gi"
          blockSize: 4096
```

## Usage

### Access S3 API

```bash
# Port-forward to access S3 API locally
kubectl port-forward svc/storage-appliance 9000:9000

# Configure AWS CLI
aws configure set aws_access_key_id admin
aws configure set aws_secret_access_key password
aws configure set default.region us-east-1
aws configure set default.s3.endpoint_url http://localhost:9000

# Create bucket and upload object
aws s3 mb s3://my-bucket
aws s3 cp file.txt s3://my-bucket/
```

### Access NVMe-oF

```bash
# Discover subsystems
kubectl exec -it storage-appliance-0 -- \
  nvme discover -t tcp -a <pod-ip> -s 4420

# Connect to subsystem
kubectl exec -it storage-appliance-0 -- \
  nvme connect -t tcp -n nqn.2024-01.com.storage:subsys1 \
    -a <pod-ip> -s 4420
```

### Monitor Performance

```bash
# Access Prometheus metrics
kubectl port-forward svc/storage-appliance 9090:9090

# View metrics in browser
open http://localhost:9090/metrics
```

## Scaling

### Scale Replicas

```bash
# Scale to 5 replicas
helm upgrade storage-appliance ./helm/storage-appliance \
  --set replicaCount=5
```

### Scale Storage

```bash
# Increase storage per pod to 1TB
helm upgrade storage-appliance ./helm/storage-appliance \
  --set persistence.size=1Ti
```

## Monitoring

The chart includes built-in monitoring support:

- **Prometheus metrics** exposed on port 9090
- **ServiceMonitor** for automatic Prometheus scraping
- **Grafana dashboards** (when enabled)

## Security

Security features included:

- Non-root containers
- Read-only root filesystem
- Capability dropping
- Pod security contexts
- Network policies
- Resource limits

## Upgrades

```bash
# Upgrade to new version
helm upgrade storage-appliance ./helm/storage-appliance \
  --set image.tag=2.0.0

# Rollback if needed
helm rollback storage-appliance
```

## Uninstallation

```bash
helm uninstall storage-appliance
```

**Note:** This will not delete PersistentVolumeClaims. Delete them manually if needed:

```bash
kubectl delete pvc -l app.kubernetes.io/name=storage-appliance
```

## Troubleshooting

### Check Pod Status

```bash
kubectl get pods -l app.kubernetes.io/name=storage-appliance
kubectl describe pod storage-appliance-0
kubectl logs storage-appliance-0
```

### Check PersistentVolumeClaims

```bash
kubectl get pvc
kubectl describe pvc data-storage-appliance-0
```

### Debug Connection Issues

```bash
# Test S3 connectivity
kubectl run -it --rm debug --image=amazon/aws-cli --restart=Never -- \
  s3 ls --endpoint-url http://storage-appliance:9000

# Test NVMe-oF connectivity
kubectl run -it --rm debug --image=nvme-cli --restart=Never -- \
  nvme discover -t tcp -a storage-appliance -s 4420
```

## Performance Tuning

### High-Performance Configuration

```yaml
resources:
  limits:
    cpu: "8"
    memory: "16Gi"
  requests:
    cpu: "4"
    memory: "8Gi"

persistence:
  storageClass: "fast-nvme"
  size: "2Ti"

nodeSelector:
  storage-node: "true"
  nvme-devices: "true"
```

### Enable CGO Mode (Hardware Acceleration)

```yaml
image:
  tag: "cgo-latest"

nodeSelector:
  spdk-enabled: "true"

tolerations:
  - key: "storage"
    operator: "Equal"
    value: "dedicated"
    effect: "NoSchedule"
```

## License

See LICENSE file for details.

## Support

For issues and questions:
- GitHub: https://github.com/srikommareddi/Storage-Appliance-Architecture
- Issues: https://github.com/srikommareddi/Storage-Appliance-Architecture/issues
