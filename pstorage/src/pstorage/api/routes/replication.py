"""Replication and Cluster API routes."""

from fastapi import APIRouter, HTTPException, status

from ..schemas.replication import (
    AddPeerRequest,
    ApplyReplicationRequest,
    ClusterStatusResponse,
    FailoverRequest,
    FailoverResponse,
    HeartbeatRequest,
    HeartbeatResponse,
    PeerNode,
    ReplicationStatusResponse,
)
from ...activecluster.cluster import get_cluster
from ...core.config import get_settings

router = APIRouter(prefix="/cluster", tags=["cluster"])


@router.get("/status", response_model=ClusterStatusResponse)
async def get_cluster_status():
    """Get cluster status."""
    cluster = await get_cluster()

    if not cluster:
        settings = get_settings()
        return ClusterStatusResponse(
            node_id=settings.node_id,
            state="standalone",
            current_primary=settings.node_id,
            has_quorum=True,
            node_count=1,
            healthy_nodes=1,
            replication_lag_ms=0.0,
        )

    health = await cluster.get_health()

    return ClusterStatusResponse(
        node_id=cluster.node_id,
        state=health.state.value,
        current_primary=health.primary_node,
        has_quorum=health.has_quorum,
        node_count=health.node_count,
        healthy_nodes=health.healthy_nodes,
        replication_lag_ms=health.replication_lag_ms,
    )


@router.get("/replication", response_model=ReplicationStatusResponse)
async def get_replication_status():
    """Get replication status."""
    cluster = await get_cluster()

    if not cluster:
        settings = get_settings()
        return ReplicationStatusResponse(
            mode="disabled",
            node_id=settings.node_id,
            is_primary=True,
            peers=[],
            operations_replicated=0,
            operations_pending=0,
            avg_latency_ms=0.0,
        )

    stats = await cluster.replication.get_stats()
    is_primary = await cluster.is_primary()

    peers = []
    for peer_id, state in stats.get("peers", {}).items():
        peer = await cluster.replication.get_peer_status(peer_id)
        if peer:
            peers.append(
                PeerNode(
                    node_id=peer.node_id,
                    url=peer.url,
                    state=peer.state.value,
                    last_heartbeat=peer.last_heartbeat,
                    lag_bytes=peer.lag_bytes,
                )
            )

    return ReplicationStatusResponse(
        mode=stats["mode"],
        node_id=cluster.node_id,
        is_primary=is_primary,
        peers=peers,
        operations_replicated=stats["operations_replicated"],
        operations_pending=stats["operations_pending"],
        avg_latency_ms=stats["avg_latency_ms"],
    )


@router.post("/peers", response_model=PeerNode, status_code=status.HTTP_201_CREATED)
async def add_peer(request: AddPeerRequest):
    """Add a replication peer."""
    cluster = await get_cluster()

    if not cluster:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="Clustering is not enabled",
        )

    peer = await cluster.replication.add_peer(request.node_id, request.url)

    return PeerNode(
        node_id=peer.node_id,
        url=peer.url,
        state=peer.state.value,
        last_heartbeat=peer.last_heartbeat,
        lag_bytes=peer.lag_bytes,
    )


@router.delete("/peers/{node_id}", status_code=status.HTTP_204_NO_CONTENT)
async def remove_peer(node_id: str):
    """Remove a replication peer."""
    cluster = await get_cluster()

    if not cluster:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="Clustering is not enabled",
        )

    if not await cluster.replication.remove_peer(node_id):
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Peer {node_id} not found",
        )


@router.post("/failover", response_model=FailoverResponse)
async def trigger_failover(request: FailoverRequest):
    """Manually trigger failover."""
    cluster = await get_cluster()

    if not cluster:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="Clustering is not enabled",
        )

    event = await cluster.manual_failover(request.target_node)

    return FailoverResponse(
        old_primary=event.old_primary,
        new_primary=event.new_primary,
        reason=event.reason.value,
        state=event.state.value,
        duration_ms=event.duration_ms,
    )


@router.post("/failback", response_model=FailoverResponse)
async def trigger_failback():
    """Manually trigger failback to original primary."""
    cluster = await get_cluster()

    if not cluster:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="Clustering is not enabled",
        )

    event = await cluster.manual_failback()

    if not event:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="No failback available (not in failed-over state)",
        )

    return FailoverResponse(
        old_primary=event.old_primary,
        new_primary=event.new_primary,
        reason=event.reason.value,
        state=event.state.value,
        duration_ms=event.duration_ms,
    )


@router.post("/heartbeat", response_model=HeartbeatResponse)
async def receive_heartbeat(request: HeartbeatRequest):
    """Receive heartbeat from peer node."""
    cluster = await get_cluster()

    if not cluster:
        settings = get_settings()
        return HeartbeatResponse(
            node_id=settings.node_id,
            current_primary=settings.node_id,
            timestamp=request.timestamp,
        )

    response = await cluster.mediator.receive_heartbeat(
        source_node=request.source_node,
        timestamp=request.timestamp,
        current_primary=request.current_primary,
    )

    return HeartbeatResponse(
        node_id=response["node_id"],
        current_primary=response["current_primary"],
        timestamp=response["timestamp"],
    )


@router.post("/replication/apply", status_code=status.HTTP_200_OK)
async def apply_replication(request: ApplyReplicationRequest):
    """Apply a replicated operation from a peer."""
    # This would be implemented to actually apply the operation
    # For now, just acknowledge receipt
    return {"status": "ok", "sequence_number": request.sequence_number}
