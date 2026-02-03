"""ActiveCluster - Main cluster management interface."""

import asyncio
from dataclasses import dataclass
from enum import Enum

from ..core.config import Settings, get_settings
from .failover import FailoverController
from .mediator import Mediator, NodeRole
from .replication import ReplicationEngine, ReplicationMode


class ClusterState(Enum):
    """Overall cluster state."""

    INITIALIZING = "initializing"
    HEALTHY = "healthy"
    DEGRADED = "degraded"
    FAILED = "failed"
    MAINTENANCE = "maintenance"


@dataclass
class ClusterHealth:
    """Cluster health summary."""

    state: ClusterState
    node_count: int
    healthy_nodes: int
    has_quorum: bool
    primary_node: str | None
    replication_lag_ms: float


class ActiveCluster:
    """
    ActiveCluster - High Availability Manager.

    Provides active-active clustering with:
    - Synchronous replication between nodes
    - Automatic failover with zero data loss
    - Quorum-based split-brain prevention
    - Transparent failback

    Inspired by Pure Storage Purity ActiveCluster.
    """

    def __init__(self, settings: Settings | None = None):
        self.settings = settings or get_settings()

        self.node_id = self.settings.node_id

        # Initialize components
        self.mediator = Mediator(
            node_id=self.node_id,
            heartbeat_interval_ms=self.settings.heartbeat_interval_ms,
            failure_threshold_ms=self.settings.failover_timeout_ms,
        )

        replication_mode = ReplicationMode(self.settings.replication_mode)
        self.replication = ReplicationEngine(
            node_id=self.node_id,
            mode=replication_mode,
            timeout_ms=self.settings.failover_timeout_ms,
        )

        self.failover = FailoverController(
            mediator=self.mediator,
            failover_timeout_ms=self.settings.failover_timeout_ms,
        )

        self._state = ClusterState.INITIALIZING
        self._initialized = False
        self._monitor_task: asyncio.Task | None = None

    async def initialize(self, role: NodeRole = NodeRole.PRIMARY) -> None:
        """Initialize the cluster node."""
        if self._initialized:
            return

        # Register self with mediator
        api_url = f"http://{self.settings.api_host}:{self.settings.api_port}"
        await self.mediator.register_self(role, api_url)

        # Add peer nodes
        for peer_url in self.settings.peer_nodes:
            # Parse peer URL to extract node_id (simplified)
            peer_id = f"node-{hash(peer_url) % 1000}"
            await self.mediator.add_node(peer_id, NodeRole.SECONDARY, peer_url)
            await self.replication.add_peer(peer_id, peer_url)

        # Set initial primary if we're the first node
        if role == NodeRole.PRIMARY:
            await self.mediator.set_primary(self.node_id)

        # Start services
        await self.mediator.start_heartbeat()

        if self.replication.mode == ReplicationMode.ASYNC:
            await self.replication.start_async_replication()

        # Start cluster monitor
        self._monitor_task = asyncio.create_task(self._monitor_cluster())

        self._initialized = True
        self._state = ClusterState.HEALTHY

    async def shutdown(self) -> None:
        """Shutdown the cluster node."""
        self._state = ClusterState.MAINTENANCE

        if self._monitor_task:
            self._monitor_task.cancel()
            try:
                await self._monitor_task
            except asyncio.CancelledError:
                pass

        await self.mediator.close()
        await self.replication.close()

        self._initialized = False

    async def _monitor_cluster(self) -> None:
        """Background cluster health monitoring."""
        while True:
            try:
                # Check for failover needs
                await self.failover.check_and_failover()

                # Check for failback opportunity
                await self.failover.check_and_failback()

                # Update cluster state
                await self._update_state()

                await asyncio.sleep(1.0)

            except asyncio.CancelledError:
                break
            except Exception:
                await asyncio.sleep(5.0)

    async def _update_state(self) -> None:
        """Update overall cluster state."""
        status = await self.mediator.get_cluster_status()

        if not status["has_quorum"]:
            self._state = ClusterState.FAILED
        elif status["current_primary"] is None:
            self._state = ClusterState.DEGRADED
        else:
            healthy = sum(
                1 for n in status["nodes"].values() if n["is_healthy"]
            )
            total = len(status["nodes"])

            if healthy == total:
                self._state = ClusterState.HEALTHY
            elif healthy > 0:
                self._state = ClusterState.DEGRADED
            else:
                self._state = ClusterState.FAILED

    async def is_primary(self) -> bool:
        """Check if this node is the primary."""
        primary = await self.mediator.get_primary()
        return primary == self.node_id

    async def get_primary(self) -> str | None:
        """Get the current primary node ID."""
        return await self.mediator.get_primary()

    async def replicate_write(
        self,
        volume_id: str,
        offset: int,
        block_id: str,
        fingerprint: str,
        data: bytes,
    ) -> bool:
        """Replicate a write operation to peer nodes."""
        return await self.replication.replicate_write(
            volume_id=volume_id,
            offset=offset,
            block_id=block_id,
            fingerprint=fingerprint,
            data=data,
        )

    async def replicate_delete(self, volume_id: str, block_id: str) -> bool:
        """Replicate a delete operation to peer nodes."""
        return await self.replication.replicate_delete(volume_id, block_id)

    async def manual_failover(self, target_node: str | None = None):
        """Manually trigger failover."""
        return await self.failover.manual_failover(target_node)

    async def manual_failback(self):
        """Manually trigger failback to original primary."""
        return await self.failover.manual_failback()

    async def get_health(self) -> ClusterHealth:
        """Get cluster health summary."""
        status = await self.mediator.get_cluster_status()
        replication_stats = await self.replication.get_stats()

        healthy_nodes = sum(
            1 for n in status["nodes"].values() if n["is_healthy"]
        )

        return ClusterHealth(
            state=self._state,
            node_count=len(status["nodes"]),
            healthy_nodes=healthy_nodes,
            has_quorum=status["has_quorum"],
            primary_node=status["current_primary"],
            replication_lag_ms=replication_stats["avg_latency_ms"],
        )

    async def get_stats(self) -> dict:
        """Get comprehensive cluster statistics."""
        cluster_status = await self.mediator.get_cluster_status()
        mediator_stats = await self.mediator.get_stats()
        replication_stats = await self.replication.get_stats()
        failover_stats = await self.failover.get_stats()

        return {
            "node_id": self.node_id,
            "state": self._state.value,
            "cluster": cluster_status,
            "mediator": mediator_stats,
            "replication": replication_stats,
            "failover": failover_stats,
        }


# Singleton cluster instance
_cluster: ActiveCluster | None = None


async def get_cluster() -> ActiveCluster | None:
    """Get the cluster instance (None if clustering disabled)."""
    global _cluster
    settings = get_settings()

    if not settings.cluster_enabled:
        return None

    if _cluster is None:
        _cluster = ActiveCluster(settings)
        await _cluster.initialize()

    return _cluster
