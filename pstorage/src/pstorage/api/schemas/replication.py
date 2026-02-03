"""Replication API schemas."""

from pydantic import BaseModel, Field


class PeerNode(BaseModel):
    """Peer node information."""

    node_id: str
    url: str
    state: str
    last_heartbeat: float
    lag_bytes: int = 0


class AddPeerRequest(BaseModel):
    """Request to add a replication peer."""

    node_id: str
    url: str = Field(..., pattern=r"^https?://")


class ReplicationStatusResponse(BaseModel):
    """Replication status response."""

    mode: str
    node_id: str
    is_primary: bool
    peers: list[PeerNode]
    operations_replicated: int
    operations_pending: int
    avg_latency_ms: float


class ClusterStatusResponse(BaseModel):
    """Cluster status response."""

    node_id: str
    state: str
    current_primary: str | None
    has_quorum: bool
    node_count: int
    healthy_nodes: int
    replication_lag_ms: float


class FailoverRequest(BaseModel):
    """Request to initiate failover."""

    target_node: str | None = None  # If None, auto-select


class FailoverResponse(BaseModel):
    """Failover response."""

    old_primary: str | None
    new_primary: str | None
    reason: str
    state: str
    duration_ms: float | None


class HeartbeatRequest(BaseModel):
    """Heartbeat request from peer node."""

    source_node: str
    timestamp: float
    current_primary: str | None


class HeartbeatResponse(BaseModel):
    """Heartbeat response."""

    node_id: str
    current_primary: str | None
    timestamp: float


class ApplyReplicationRequest(BaseModel):
    """Request to apply replicated operation."""

    source_node: str
    sequence_number: int
    operation_type: str
    volume_id: str
    data: dict
    timestamp: float
