"""Replication Engine - Synchronous and asynchronous data replication."""

import asyncio
import time
from dataclasses import dataclass, field
from enum import Enum
from typing import Callable

import httpx


class ReplicationMode(Enum):
    """Replication mode."""

    SYNC = "sync"  # Synchronous - write to both before ack
    ASYNC = "async"  # Asynchronous - ack after primary, replicate later


class ReplicationState(Enum):
    """Replication link state."""

    HEALTHY = "healthy"
    DEGRADED = "degraded"  # Replication lagging
    BROKEN = "broken"  # Link down
    SYNCING = "syncing"  # Catching up


@dataclass
class ReplicationPeer:
    """Remote replication peer."""

    node_id: str
    url: str
    state: ReplicationState = ReplicationState.HEALTHY
    last_heartbeat: float = 0.0
    lag_bytes: int = 0
    lag_operations: int = 0


@dataclass
class ReplicationOperation:
    """Operation to replicate."""

    sequence_number: int
    operation_type: str  # "write", "delete", "snapshot"
    volume_id: str
    data: dict  # Operation-specific data
    timestamp: float = field(default_factory=time.time)


@dataclass
class ReplicationStats:
    """Replication statistics."""

    operations_replicated: int = 0
    bytes_replicated: int = 0
    operations_pending: int = 0
    replication_errors: int = 0
    avg_latency_ms: float = 0.0


class ReplicationEngine:
    """
    Replication engine for data synchronization.

    Supports:
    - Synchronous replication (write to all replicas before ack)
    - Asynchronous replication (ack after primary, replicate in background)
    - Journal-based replication for recovery
    """

    def __init__(
        self,
        node_id: str,
        mode: ReplicationMode = ReplicationMode.SYNC,
        timeout_ms: int = 5000,
    ):
        self.node_id = node_id
        self.mode = mode
        self.timeout_ms = timeout_ms

        # Peer nodes
        self._peers: dict[str, ReplicationPeer] = {}

        # Replication journal (operations waiting to be replicated)
        self._journal: list[ReplicationOperation] = []
        self._sequence_number = 0

        # Async replication task
        self._replication_task: asyncio.Task | None = None

        self._stats = ReplicationStats()
        self._lock = asyncio.Lock()

        # HTTP client for replication
        self._client = httpx.AsyncClient(timeout=timeout_ms / 1000)

    async def add_peer(self, node_id: str, url: str) -> ReplicationPeer:
        """Add a replication peer."""
        peer = ReplicationPeer(
            node_id=node_id,
            url=url,
            state=ReplicationState.SYNCING,
            last_heartbeat=time.time(),
        )

        async with self._lock:
            self._peers[node_id] = peer

        return peer

    async def remove_peer(self, node_id: str) -> bool:
        """Remove a replication peer."""
        async with self._lock:
            if node_id in self._peers:
                del self._peers[node_id]
                return True
            return False

    async def replicate_write(
        self,
        volume_id: str,
        offset: int,
        block_id: str,
        fingerprint: str,
        data: bytes,
    ) -> bool:
        """
        Replicate a write operation.

        Args:
            volume_id: Volume ID
            offset: Write offset
            block_id: Block ID
            fingerprint: Dedup fingerprint
            data: Block data

        Returns:
            True if replication succeeded (or queued for async)
        """
        async with self._lock:
            self._sequence_number += 1
            operation = ReplicationOperation(
                sequence_number=self._sequence_number,
                operation_type="write",
                volume_id=volume_id,
                data={
                    "offset": offset,
                    "block_id": block_id,
                    "fingerprint": fingerprint,
                    "data": data.hex(),  # Encode for JSON
                },
            )

        if self.mode == ReplicationMode.SYNC:
            return await self._sync_replicate(operation)
        else:
            await self._queue_for_async(operation)
            return True

    async def replicate_delete(self, volume_id: str, block_id: str) -> bool:
        """Replicate a delete operation."""
        async with self._lock:
            self._sequence_number += 1
            operation = ReplicationOperation(
                sequence_number=self._sequence_number,
                operation_type="delete",
                volume_id=volume_id,
                data={"block_id": block_id},
            )

        if self.mode == ReplicationMode.SYNC:
            return await self._sync_replicate(operation)
        else:
            await self._queue_for_async(operation)
            return True

    async def _sync_replicate(self, operation: ReplicationOperation) -> bool:
        """Synchronously replicate to all peers."""
        if not self._peers:
            return True  # No peers, nothing to replicate

        start_time = time.time()
        success_count = 0

        # Send to all peers in parallel
        tasks = []
        for peer in self._peers.values():
            if peer.state != ReplicationState.BROKEN:
                tasks.append(self._send_to_peer(peer, operation))

        if tasks:
            results = await asyncio.gather(*tasks, return_exceptions=True)
            success_count = sum(1 for r in results if r is True)

        # Update stats
        latency = (time.time() - start_time) * 1000
        async with self._lock:
            self._stats.operations_replicated += 1
            self._stats.avg_latency_ms = (
                self._stats.avg_latency_ms * 0.9 + latency * 0.1
            )

        # For sync mode, require at least one successful replication
        # (or no peers)
        return success_count > 0 or not self._peers

    async def _queue_for_async(self, operation: ReplicationOperation) -> None:
        """Queue operation for async replication."""
        async with self._lock:
            self._journal.append(operation)
            self._stats.operations_pending += 1

    async def _send_to_peer(
        self, peer: ReplicationPeer, operation: ReplicationOperation
    ) -> bool:
        """Send operation to a peer."""
        try:
            response = await self._client.post(
                f"{peer.url}/api/v1/replication/apply",
                json={
                    "source_node": self.node_id,
                    "sequence_number": operation.sequence_number,
                    "operation_type": operation.operation_type,
                    "volume_id": operation.volume_id,
                    "data": operation.data,
                    "timestamp": operation.timestamp,
                },
            )

            if response.status_code == 200:
                peer.state = ReplicationState.HEALTHY
                peer.last_heartbeat = time.time()
                return True
            else:
                self._stats.replication_errors += 1
                return False

        except Exception:
            peer.state = ReplicationState.DEGRADED
            self._stats.replication_errors += 1
            return False

    async def start_async_replication(self) -> None:
        """Start background async replication task."""
        if self._replication_task is not None:
            return

        async def replication_loop():
            while True:
                try:
                    await self._process_journal()
                    await asyncio.sleep(0.1)  # 100ms interval
                except asyncio.CancelledError:
                    break
                except Exception:
                    await asyncio.sleep(1.0)  # Back off on error

        self._replication_task = asyncio.create_task(replication_loop())

    async def stop_async_replication(self) -> None:
        """Stop background async replication."""
        if self._replication_task is not None:
            self._replication_task.cancel()
            try:
                await self._replication_task
            except asyncio.CancelledError:
                pass
            self._replication_task = None

    async def _process_journal(self) -> None:
        """Process pending operations in the journal."""
        async with self._lock:
            if not self._journal:
                return

            operations = self._journal.copy()

        for operation in operations:
            success = await self._sync_replicate(operation)
            if success:
                async with self._lock:
                    if operation in self._journal:
                        self._journal.remove(operation)
                        self._stats.operations_pending -= 1

    async def get_peer_status(self, node_id: str) -> ReplicationPeer | None:
        """Get status of a peer."""
        return self._peers.get(node_id)

    async def get_stats(self) -> dict:
        """Get replication statistics."""
        async with self._lock:
            peer_states = {
                peer_id: peer.state.value for peer_id, peer in self._peers.items()
            }

            return {
                "mode": self.mode.value,
                "peers": peer_states,
                "operations_replicated": self._stats.operations_replicated,
                "bytes_replicated": self._stats.bytes_replicated,
                "operations_pending": self._stats.operations_pending,
                "replication_errors": self._stats.replication_errors,
                "avg_latency_ms": self._stats.avg_latency_ms,
                "journal_size": len(self._journal),
            }

    async def close(self) -> None:
        """Close the replication engine."""
        await self.stop_async_replication()
        await self._client.aclose()
