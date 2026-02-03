"""Volume Manager - Manages storage volumes."""

import asyncio
import uuid
from dataclasses import dataclass, field
from datetime import datetime
from enum import Enum
from typing import AsyncIterator


class VolumeState(Enum):
    """Volume state."""

    CREATING = "creating"
    AVAILABLE = "available"
    IN_USE = "in_use"
    DELETING = "deleting"
    ERROR = "error"


@dataclass
class VolumeBlock:
    """Represents a block within a volume."""

    offset: int  # Offset within volume
    block_id: str  # Reference to stored block
    fingerprint: str  # Dedup fingerprint
    size: int


@dataclass
class Volume:
    """Storage volume."""

    id: str
    name: str
    size_bytes: int  # Provisioned size
    created_at: datetime
    updated_at: datetime
    state: VolumeState = VolumeState.AVAILABLE
    description: str = ""

    # Block mapping (offset -> VolumeBlock)
    blocks: dict[int, VolumeBlock] = field(default_factory=dict)

    # Statistics
    used_bytes: int = 0
    logical_bytes: int = 0  # Before reduction
    physical_bytes: int = 0  # After reduction

    # Metadata
    tags: dict[str, str] = field(default_factory=dict)

    @property
    def reduction_ratio(self) -> float:
        """Get data reduction ratio."""
        if self.physical_bytes == 0:
            return 1.0
        return self.logical_bytes / self.physical_bytes


@dataclass
class VolumeStats:
    """Volume manager statistics."""

    total_volumes: int = 0
    total_provisioned_bytes: int = 0
    total_used_bytes: int = 0
    total_logical_bytes: int = 0
    total_physical_bytes: int = 0


class VolumeManager:
    """
    Manages storage volumes.

    Volumes are logical storage containers that can be:
    - Created with a provisioned size
    - Written to at specific offsets
    - Read from
    - Cloned efficiently (copy-on-write)
    - Deleted
    """

    def __init__(self, block_size: int = 4096):
        self.block_size = block_size
        self._volumes: dict[str, Volume] = {}
        self._lock = asyncio.Lock()
        self._stats = VolumeStats()

    async def create_volume(
        self,
        name: str,
        size_bytes: int,
        description: str = "",
        tags: dict[str, str] | None = None,
    ) -> Volume:
        """
        Create a new volume.

        Args:
            name: Volume name
            size_bytes: Provisioned size in bytes
            description: Optional description
            tags: Optional metadata tags

        Returns:
            Created Volume
        """
        volume_id = str(uuid.uuid4())
        now = datetime.utcnow()

        volume = Volume(
            id=volume_id,
            name=name,
            size_bytes=size_bytes,
            created_at=now,
            updated_at=now,
            state=VolumeState.AVAILABLE,
            description=description,
            tags=tags or {},
        )

        async with self._lock:
            self._volumes[volume_id] = volume
            self._stats.total_volumes += 1
            self._stats.total_provisioned_bytes += size_bytes

        return volume

    async def get_volume(self, volume_id: str) -> Volume | None:
        """Get a volume by ID."""
        return self._volumes.get(volume_id)

    async def get_volume_by_name(self, name: str) -> Volume | None:
        """Get a volume by name."""
        for volume in self._volumes.values():
            if volume.name == name:
                return volume
        return None

    async def list_volumes(self) -> AsyncIterator[Volume]:
        """List all volumes."""
        for volume in self._volumes.values():
            yield volume

    async def update_volume(
        self,
        volume_id: str,
        name: str | None = None,
        description: str | None = None,
        tags: dict[str, str] | None = None,
    ) -> Volume | None:
        """Update volume metadata."""
        async with self._lock:
            volume = self._volumes.get(volume_id)
            if not volume:
                return None

            if name is not None:
                volume.name = name
            if description is not None:
                volume.description = description
            if tags is not None:
                volume.tags = tags

            volume.updated_at = datetime.utcnow()
            return volume

    async def delete_volume(self, volume_id: str) -> bool:
        """
        Delete a volume.

        Note: Actual block cleanup should be handled by caller.
        """
        async with self._lock:
            volume = self._volumes.get(volume_id)
            if not volume:
                return False

            volume.state = VolumeState.DELETING

            # Update stats
            self._stats.total_volumes -= 1
            self._stats.total_provisioned_bytes -= volume.size_bytes
            self._stats.total_used_bytes -= volume.used_bytes
            self._stats.total_logical_bytes -= volume.logical_bytes
            self._stats.total_physical_bytes -= volume.physical_bytes

            del self._volumes[volume_id]
            return True

    async def resize_volume(self, volume_id: str, new_size_bytes: int) -> Volume | None:
        """
        Resize a volume.

        Note: Only expansion is supported (no shrinking).
        """
        async with self._lock:
            volume = self._volumes.get(volume_id)
            if not volume:
                return None

            if new_size_bytes < volume.size_bytes:
                raise ValueError("Cannot shrink volume")

            old_size = volume.size_bytes
            volume.size_bytes = new_size_bytes
            volume.updated_at = datetime.utcnow()

            self._stats.total_provisioned_bytes += new_size_bytes - old_size

            return volume

    async def write_block(
        self,
        volume_id: str,
        offset: int,
        block_id: str,
        fingerprint: str,
        size: int,
        physical_size: int,
    ) -> bool:
        """
        Record a block write to a volume.

        Args:
            volume_id: Volume ID
            offset: Offset within volume
            block_id: Stored block ID
            fingerprint: Dedup fingerprint
            size: Logical size
            physical_size: Physical size after reduction
        """
        async with self._lock:
            volume = self._volumes.get(volume_id)
            if not volume:
                return False

            if offset + size > volume.size_bytes:
                raise ValueError("Write exceeds volume size")

            # Check if overwriting existing block
            old_block = volume.blocks.get(offset)
            if old_block:
                volume.logical_bytes -= old_block.size
                volume.used_bytes -= old_block.size

            # Add new block
            volume.blocks[offset] = VolumeBlock(
                offset=offset,
                block_id=block_id,
                fingerprint=fingerprint,
                size=size,
            )

            volume.used_bytes += size
            volume.logical_bytes += size
            volume.physical_bytes = physical_size  # Updated by caller
            volume.updated_at = datetime.utcnow()

            self._stats.total_used_bytes += size
            self._stats.total_logical_bytes += size

            return True

    async def get_block(self, volume_id: str, offset: int) -> VolumeBlock | None:
        """Get block at specific offset."""
        volume = self._volumes.get(volume_id)
        if not volume:
            return None
        return volume.blocks.get(offset)

    async def clone_volume(
        self, source_id: str, new_name: str, description: str = ""
    ) -> Volume | None:
        """
        Clone a volume (copy-on-write).

        Creates a new volume that shares blocks with the source.
        """
        source = self._volumes.get(source_id)
        if not source:
            return None

        # Create new volume with same metadata
        clone = await self.create_volume(
            name=new_name,
            size_bytes=source.size_bytes,
            description=description or f"Clone of {source.name}",
            tags=source.tags.copy(),
        )

        # Copy block references (not actual data - copy-on-write)
        async with self._lock:
            for offset, block in source.blocks.items():
                clone.blocks[offset] = VolumeBlock(
                    offset=block.offset,
                    block_id=block.block_id,
                    fingerprint=block.fingerprint,
                    size=block.size,
                )

            clone.used_bytes = source.used_bytes
            clone.logical_bytes = source.logical_bytes
            clone.physical_bytes = 0  # Clone doesn't add physical storage

        return clone

    async def get_stats(self) -> dict:
        """Get volume manager statistics."""
        return {
            "total_volumes": self._stats.total_volumes,
            "total_provisioned_bytes": self._stats.total_provisioned_bytes,
            "total_used_bytes": self._stats.total_used_bytes,
            "total_logical_bytes": self._stats.total_logical_bytes,
            "total_physical_bytes": self._stats.total_physical_bytes,
        }
