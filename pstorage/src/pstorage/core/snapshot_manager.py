"""Snapshot Manager - Point-in-time volume snapshots."""

import asyncio
import uuid
from dataclasses import dataclass, field
from datetime import datetime
from enum import Enum
from typing import AsyncIterator

from .volume_manager import Volume, VolumeBlock


class SnapshotState(Enum):
    """Snapshot state."""

    CREATING = "creating"
    AVAILABLE = "available"
    DELETING = "deleting"
    ERROR = "error"


@dataclass
class Snapshot:
    """Point-in-time snapshot of a volume."""

    id: str
    name: str
    volume_id: str
    volume_name: str
    created_at: datetime
    state: SnapshotState = SnapshotState.AVAILABLE
    description: str = ""

    # Block mapping at snapshot time
    blocks: dict[int, VolumeBlock] = field(default_factory=dict)

    # Size info
    size_bytes: int = 0  # Volume size at snapshot time
    used_bytes: int = 0

    # Metadata
    tags: dict[str, str] = field(default_factory=dict)


@dataclass
class SnapshotStats:
    """Snapshot manager statistics."""

    total_snapshots: int = 0
    snapshots_by_volume: dict[str, int] = field(default_factory=dict)


class SnapshotManager:
    """
    Manages volume snapshots.

    Snapshots are:
    - Point-in-time copies of volume state
    - Space-efficient (only metadata, shared blocks with source)
    - Immutable once created
    - Can be used to restore or clone volumes
    """

    def __init__(self):
        self._snapshots: dict[str, Snapshot] = {}
        self._volume_snapshots: dict[str, list[str]] = {}  # volume_id -> [snapshot_ids]
        self._lock = asyncio.Lock()
        self._stats = SnapshotStats()

    async def create_snapshot(
        self,
        volume: Volume,
        name: str | None = None,
        description: str = "",
        tags: dict[str, str] | None = None,
    ) -> Snapshot:
        """
        Create a snapshot of a volume.

        Args:
            volume: Volume to snapshot
            name: Optional snapshot name (auto-generated if not provided)
            description: Optional description
            tags: Optional metadata tags

        Returns:
            Created Snapshot
        """
        snapshot_id = str(uuid.uuid4())
        now = datetime.utcnow()

        if not name:
            # Generate name based on volume and timestamp
            timestamp = now.strftime("%Y%m%d-%H%M%S")
            name = f"{volume.name}-snap-{timestamp}"

        snapshot = Snapshot(
            id=snapshot_id,
            name=name,
            volume_id=volume.id,
            volume_name=volume.name,
            created_at=now,
            state=SnapshotState.AVAILABLE,
            description=description,
            size_bytes=volume.size_bytes,
            used_bytes=volume.used_bytes,
            tags=tags or {},
        )

        # Copy block references (metadata only, not actual data)
        for offset, block in volume.blocks.items():
            snapshot.blocks[offset] = VolumeBlock(
                offset=block.offset,
                block_id=block.block_id,
                fingerprint=block.fingerprint,
                size=block.size,
            )

        async with self._lock:
            self._snapshots[snapshot_id] = snapshot

            # Track snapshots by volume
            if volume.id not in self._volume_snapshots:
                self._volume_snapshots[volume.id] = []
            self._volume_snapshots[volume.id].append(snapshot_id)

            self._stats.total_snapshots += 1
            self._stats.snapshots_by_volume[volume.id] = len(
                self._volume_snapshots[volume.id]
            )

        return snapshot

    async def get_snapshot(self, snapshot_id: str) -> Snapshot | None:
        """Get a snapshot by ID."""
        async with self._lock:
            return self._snapshots.get(snapshot_id)

    async def get_snapshot_by_name(
        self, volume_id: str, name: str
    ) -> Snapshot | None:
        """Get a snapshot by name within a volume."""
        async with self._lock:
            snapshot_ids = self._volume_snapshots.get(volume_id, [])
            for sid in snapshot_ids:
                snapshot = self._snapshots.get(sid)
                if snapshot and snapshot.name == name:
                    return snapshot
            return None

    async def list_snapshots(
        self, volume_id: str | None = None
    ) -> AsyncIterator[Snapshot]:
        """
        List snapshots.

        Args:
            volume_id: Optional filter by volume ID
        """
        async with self._lock:
            if volume_id:
                snapshot_ids = list(self._volume_snapshots.get(volume_id, []))
                snapshots = [self._snapshots.get(sid) for sid in snapshot_ids]
            else:
                snapshots = list(self._snapshots.values())

        for snapshot in snapshots:
            if snapshot:
                yield snapshot

    async def delete_snapshot(self, snapshot_id: str) -> bool:
        """
        Delete a snapshot.

        Note: Actual block cleanup (deref) should be handled by caller.
        """
        async with self._lock:
            snapshot = self._snapshots.get(snapshot_id)
            if not snapshot:
                return False

            snapshot.state = SnapshotState.DELETING

            # Remove from volume tracking
            if snapshot.volume_id in self._volume_snapshots:
                self._volume_snapshots[snapshot.volume_id].remove(snapshot_id)
                self._stats.snapshots_by_volume[snapshot.volume_id] = len(
                    self._volume_snapshots[snapshot.volume_id]
                )

            del self._snapshots[snapshot_id]
            self._stats.total_snapshots -= 1

            return True

    async def get_volume_snapshots(self, volume_id: str) -> list[Snapshot]:
        """Get all snapshots for a volume, sorted by creation time."""
        snapshots = []
        async for snapshot in self.list_snapshots(volume_id):
            snapshots.append(snapshot)
        return sorted(snapshots, key=lambda s: s.created_at)

    async def get_latest_snapshot(self, volume_id: str) -> Snapshot | None:
        """Get the most recent snapshot for a volume."""
        snapshots = await self.get_volume_snapshots(volume_id)
        return snapshots[-1] if snapshots else None

    async def get_snapshot_blocks(
        self, snapshot_id: str
    ) -> dict[int, VolumeBlock] | None:
        """Get block mapping from a snapshot."""
        async with self._lock:
            snapshot = self._snapshots.get(snapshot_id)
            if not snapshot:
                return None
            return snapshot.blocks.copy()

    async def get_stats(self) -> dict:
        """Get snapshot manager statistics."""
        async with self._lock:
            return {
                "total_snapshots": self._stats.total_snapshots,
                "snapshots_by_volume": dict(self._stats.snapshots_by_volume),
            }
