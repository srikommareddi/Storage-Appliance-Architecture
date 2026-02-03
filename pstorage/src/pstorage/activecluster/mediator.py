"""Mediator - Split-brain prevention and quorum management."""

import asyncio
import time
from dataclasses import dataclass
from enum import Enum

import httpx


class NodeRole(Enum):
    """Node role in the cluster."""

    PRIMARY = "primary"
    SECONDARY = "secondary"
    WITNESS = "witness"  # Mediator-only node


class QuorumState(Enum):
    """Quorum state."""

    HAS_QUORUM = "has_quorum"
    NO_QUORUM = "no_quorum"
    SPLIT_BRAIN = "split_brain"


@dataclass
class NodeInfo:
    """Information about a cluster node."""

    node_id: str
    role: NodeRole
    url: str
    is_healthy: bool = True
    last_heartbeat: float = 0.0
    vote: str | None = None  # Which node this node votes for as primary


@dataclass
class MediatorStats:
    """Mediator statistics."""

    heartbeats_sent: int = 0
    heartbeats_received: int = 0
    elections_held: int = 0
    split_brains_prevented: int = 0


class Mediator:
    """
    Cluster mediator for quorum and split-brain prevention.

    The mediator:
    - Maintains cluster membership
    - Monitors node health via heartbeats
    - Manages quorum (majority vote for decisions)
    - Prevents split-brain by arbitrating primary selection
    - Coordinates failover decisions

    Based on Pure Storage's Pure1 Cloud Mediator concept.
    """

    def __init__(
        self,
        node_id: str,
        heartbeat_interval_ms: int = 1000,
        failure_threshold_ms: int = 5000,
    ):
        self.node_id = node_id
        self.heartbeat_interval_ms = heartbeat_interval_ms
        self.failure_threshold_ms = failure_threshold_ms

        # Cluster nodes
        self._nodes: dict[str, NodeInfo] = {}
        self._self_info: NodeInfo | None = None

        # Current primary
        self._current_primary: str | None = None

        self._heartbeat_task: asyncio.Task | None = None
        self._stats = MediatorStats()
        self._lock = asyncio.Lock()

        # HTTP client
        self._client = httpx.AsyncClient(timeout=heartbeat_interval_ms / 1000)

    async def register_self(self, role: NodeRole, url: str) -> None:
        """Register this node with the mediator."""
        self._self_info = NodeInfo(
            node_id=self.node_id,
            role=role,
            url=url,
            is_healthy=True,
            last_heartbeat=time.time(),
        )

        async with self._lock:
            self._nodes[self.node_id] = self._self_info

    async def add_node(self, node_id: str, role: NodeRole, url: str) -> NodeInfo:
        """Add a node to the cluster."""
        node = NodeInfo(
            node_id=node_id,
            role=role,
            url=url,
            is_healthy=True,
            last_heartbeat=time.time(),
        )

        async with self._lock:
            self._nodes[node_id] = node

        return node

    async def remove_node(self, node_id: str) -> bool:
        """Remove a node from the cluster."""
        async with self._lock:
            if node_id in self._nodes:
                del self._nodes[node_id]
                return True
            return False

    async def start_heartbeat(self) -> None:
        """Start heartbeat monitoring."""
        if self._heartbeat_task is not None:
            return

        async def heartbeat_loop():
            while True:
                try:
                    await self._send_heartbeats()
                    await self._check_node_health()
                    await asyncio.sleep(self.heartbeat_interval_ms / 1000)
                except asyncio.CancelledError:
                    break
                except Exception:
                    await asyncio.sleep(1.0)

        self._heartbeat_task = asyncio.create_task(heartbeat_loop())

    async def stop_heartbeat(self) -> None:
        """Stop heartbeat monitoring."""
        if self._heartbeat_task is not None:
            self._heartbeat_task.cancel()
            try:
                await self._heartbeat_task
            except asyncio.CancelledError:
                pass
            self._heartbeat_task = None

    async def _send_heartbeats(self) -> None:
        """Send heartbeats to all nodes."""
        for node_id, node in list(self._nodes.items()):
            if node_id == self.node_id:
                continue

            try:
                response = await self._client.post(
                    f"{node.url}/api/v1/cluster/heartbeat",
                    json={
                        "source_node": self.node_id,
                        "timestamp": time.time(),
                        "current_primary": self._current_primary,
                    },
                )

                if response.status_code == 200:
                    self._stats.heartbeats_sent += 1
            except Exception:
                pass  # Node might be down

    async def receive_heartbeat(
        self, source_node: str, timestamp: float, current_primary: str | None
    ) -> dict:
        """Receive heartbeat from another node."""
        async with self._lock:
            if source_node in self._nodes:
                self._nodes[source_node].last_heartbeat = time.time()
                self._nodes[source_node].is_healthy = True
                self._stats.heartbeats_received += 1

        return {
            "node_id": self.node_id,
            "current_primary": self._current_primary,
            "timestamp": time.time(),
        }

    async def _check_node_health(self) -> None:
        """Check health of all nodes based on heartbeats."""
        current_time = time.time()

        async with self._lock:
            for node_id, node in self._nodes.items():
                if node_id == self.node_id:
                    continue

                time_since_heartbeat = (
                    current_time - node.last_heartbeat
                ) * 1000

                if time_since_heartbeat > self.failure_threshold_ms:
                    if node.is_healthy:
                        node.is_healthy = False
                        # Trigger failover check if primary went down
                        if node_id == self._current_primary:
                            asyncio.create_task(self.initiate_election())

    async def get_quorum_state(self) -> QuorumState:
        """Get current quorum state."""
        async with self._lock:
            total_nodes = len(self._nodes)
            healthy_nodes = sum(1 for n in self._nodes.values() if n.is_healthy)

            # Need majority for quorum
            quorum_size = (total_nodes // 2) + 1

            if healthy_nodes >= quorum_size:
                return QuorumState.HAS_QUORUM
            else:
                return QuorumState.NO_QUORUM

    async def has_quorum(self) -> bool:
        """Check if cluster has quorum."""
        state = await self.get_quorum_state()
        return state == QuorumState.HAS_QUORUM

    async def initiate_election(self) -> str | None:
        """
        Initiate primary election.

        Returns:
            Node ID of new primary, or None if no quorum
        """
        if not await self.has_quorum():
            return None

        self._stats.elections_held += 1

        async with self._lock:
            # Simple election: choose healthy node with lowest ID
            candidates = [
                n
                for n in self._nodes.values()
                if n.is_healthy and n.role in (NodeRole.PRIMARY, NodeRole.SECONDARY)
            ]

            if not candidates:
                return None

            # Sort by node_id for deterministic selection
            candidates.sort(key=lambda n: n.node_id)
            new_primary = candidates[0]

            self._current_primary = new_primary.node_id

            # Update roles
            for node in self._nodes.values():
                if node.node_id == new_primary.node_id:
                    node.role = NodeRole.PRIMARY
                elif node.role == NodeRole.PRIMARY:
                    node.role = NodeRole.SECONDARY

            return self._current_primary

    async def get_primary(self) -> str | None:
        """Get current primary node ID."""
        return self._current_primary

    async def set_primary(self, node_id: str) -> bool:
        """Manually set the primary node."""
        async with self._lock:
            if node_id not in self._nodes:
                return False

            self._current_primary = node_id

            # Update roles
            for nid, node in self._nodes.items():
                if nid == node_id:
                    node.role = NodeRole.PRIMARY
                elif node.role == NodeRole.PRIMARY:
                    node.role = NodeRole.SECONDARY

            return True

    async def get_cluster_status(self) -> dict:
        """Get cluster status summary."""
        quorum = await self.get_quorum_state()

        async with self._lock:
            nodes_status = {
                node_id: {
                    "role": node.role.value,
                    "is_healthy": node.is_healthy,
                    "last_heartbeat": node.last_heartbeat,
                }
                for node_id, node in self._nodes.items()
            }

        return {
            "self_node_id": self.node_id,
            "current_primary": self._current_primary,
            "quorum_state": quorum.value,
            "has_quorum": quorum == QuorumState.HAS_QUORUM,
            "nodes": nodes_status,
        }

    async def get_stats(self) -> dict:
        """Get mediator statistics."""
        return {
            "heartbeats_sent": self._stats.heartbeats_sent,
            "heartbeats_received": self._stats.heartbeats_received,
            "elections_held": self._stats.elections_held,
            "split_brains_prevented": self._stats.split_brains_prevented,
        }

    async def close(self) -> None:
        """Close the mediator."""
        await self.stop_heartbeat()
        await self._client.aclose()
