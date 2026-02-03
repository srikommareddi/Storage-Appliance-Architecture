"""Storage Controller - Orchestrates all storage operations."""

import asyncio
from dataclasses import dataclass

from ..directflash.simulator import DirectFlashSimulator
from ..purity.engine import PurityEngine, ReductionResult
from ..storage.backend import StorageBackend
from ..storage.file_backend import FileBackend
from .config import Settings, get_settings
from .snapshot_manager import Snapshot, SnapshotManager
from .volume_manager import Volume, VolumeManager


@dataclass
class WriteResult:
    """Result of a write operation."""

    volume_id: str
    offset: int
    size: int
    reduction: ReductionResult


@dataclass
class ReadResult:
    """Result of a read operation."""

    volume_id: str
    offset: int
    data: bytes
    size: int


class StorageController:
    """
    Main storage controller.

    Orchestrates all storage components:
    - Purity Engine (data reduction)
    - DirectFlash Simulator (flash management)
    - Volume Manager (volume lifecycle)
    - Snapshot Manager (point-in-time copies)

    Provides a unified API for storage operations.
    """

    def __init__(self, settings: Settings | None = None):
        self.settings = settings or get_settings()

        # Initialize storage backend
        self.backend: StorageBackend = FileBackend(self.settings)

        # Initialize components
        self.purity = PurityEngine(backend=self.backend, settings=self.settings)
        self.directflash = DirectFlashSimulator(settings=self.settings)
        self.volume_manager = VolumeManager(block_size=self.settings.default_block_size)
        self.snapshot_manager = SnapshotManager()

        self._initialized = False
        self._lock = asyncio.Lock()

    async def initialize(self) -> None:
        """Initialize all storage components."""
        if self._initialized:
            return

        await self.backend.initialize()
        await self.directflash.initialize()

        self._initialized = True

    async def shutdown(self) -> None:
        """Shutdown all storage components."""
        await self.directflash.shutdown()
        await self.backend.shutdown()
        self._initialized = False

    # Volume Operations

    async def create_volume(
        self,
        name: str,
        size_gb: float,
        description: str = "",
        tags: dict[str, str] | None = None,
    ) -> Volume:
        """
        Create a new storage volume.

        Args:
            name: Volume name
            size_gb: Size in gigabytes
            description: Optional description
            tags: Optional metadata tags

        Returns:
            Created Volume
        """
        size_bytes = int(size_gb * 1024 * 1024 * 1024)
        return await self.volume_manager.create_volume(
            name=name,
            size_bytes=size_bytes,
            description=description,
            tags=tags,
        )

    async def get_volume(self, volume_id: str) -> Volume | None:
        """Get a volume by ID."""
        return await self.volume_manager.get_volume(volume_id)

    async def list_volumes(self) -> list[Volume]:
        """List all volumes."""
        volumes = []
        async for volume in self.volume_manager.list_volumes():
            volumes.append(volume)
        return volumes

    async def delete_volume(self, volume_id: str) -> bool:
        """Delete a volume and its data."""
        volume = await self.volume_manager.get_volume(volume_id)
        if not volume:
            return False

        # Delete all blocks
        for block in volume.blocks.values():
            await self.purity.delete(block.block_id, block.fingerprint)

        return await self.volume_manager.delete_volume(volume_id)

    async def clone_volume(
        self, source_id: str, new_name: str, description: str = ""
    ) -> Volume | None:
        """Clone a volume (copy-on-write)."""
        return await self.volume_manager.clone_volume(source_id, new_name, description)

    # Data Operations

    async def write(
        self, volume_id: str, offset: int, data: bytes
    ) -> WriteResult | None:
        """
        Write data to a volume.

        Args:
            volume_id: Target volume ID
            offset: Offset within volume (must be block-aligned)
            data: Data to write

        Returns:
            WriteResult or None if volume not found
        """
        volume = await self.volume_manager.get_volume(volume_id)
        if not volume:
            return None

        # Validate offset alignment
        if offset % self.settings.default_block_size != 0:
            raise ValueError(
                f"Offset must be aligned to block size ({self.settings.default_block_size})"
            )

        # Process through Purity (data reduction)
        reduction = await self.purity.reduce_and_store(data)

        # Update volume block mapping
        await self.volume_manager.write_block(
            volume_id=volume_id,
            offset=offset,
            block_id=reduction.block_id,
            fingerprint=reduction.dedupe_result.fingerprint,
            size=len(data),
            physical_size=reduction.stored_size,
        )

        return WriteResult(
            volume_id=volume_id,
            offset=offset,
            size=len(data),
            reduction=reduction,
        )

    async def read(
        self, volume_id: str, offset: int, size: int | None = None
    ) -> ReadResult | None:
        """
        Read data from a volume.

        Args:
            volume_id: Source volume ID
            offset: Offset within volume
            size: Optional size (defaults to block size)

        Returns:
            ReadResult or None if not found
        """
        volume = await self.volume_manager.get_volume(volume_id)
        if not volume:
            return None

        block = await self.volume_manager.get_block(volume_id, offset)
        if not block:
            return None

        # Retrieve data through Purity (handles decompression)
        data = await self.purity.retrieve(block.block_id, block.fingerprint)

        return ReadResult(
            volume_id=volume_id,
            offset=offset,
            data=data,
            size=len(data),
        )

    # Snapshot Operations

    async def create_snapshot(
        self,
        volume_id: str,
        name: str | None = None,
        description: str = "",
    ) -> Snapshot | None:
        """
        Create a snapshot of a volume.

        Args:
            volume_id: Volume to snapshot
            name: Optional snapshot name
            description: Optional description

        Returns:
            Created Snapshot or None if volume not found
        """
        volume = await self.volume_manager.get_volume(volume_id)
        if not volume:
            return None

        return await self.snapshot_manager.create_snapshot(
            volume=volume,
            name=name,
            description=description,
        )

    async def get_snapshot(self, snapshot_id: str) -> Snapshot | None:
        """Get a snapshot by ID."""
        return await self.snapshot_manager.get_snapshot(snapshot_id)

    async def list_snapshots(self, volume_id: str | None = None) -> list[Snapshot]:
        """List snapshots, optionally filtered by volume."""
        snapshots = []
        async for snapshot in self.snapshot_manager.list_snapshots(volume_id):
            snapshots.append(snapshot)
        return snapshots

    async def delete_snapshot(self, snapshot_id: str) -> bool:
        """Delete a snapshot."""
        snapshot = await self.snapshot_manager.get_snapshot(snapshot_id)
        if not snapshot:
            return False

        # Note: In a real implementation, we'd need to handle
        # reference counting for shared blocks. For now, just
        # delete the snapshot metadata.
        return await self.snapshot_manager.delete_snapshot(snapshot_id)

    async def restore_snapshot(
        self, snapshot_id: str, volume_id: str | None = None
    ) -> Volume | None:
        """
        Restore a volume from a snapshot.

        Args:
            snapshot_id: Snapshot to restore
            volume_id: Optional target volume (creates new if not provided)

        Returns:
            Restored/new Volume or None if snapshot not found
        """
        snapshot = await self.snapshot_manager.get_snapshot(snapshot_id)
        if not snapshot:
            return None

        if volume_id:
            # Restore to existing volume
            volume = await self.volume_manager.get_volume(volume_id)
            if not volume:
                return None

            # Clear existing blocks and restore from snapshot
            async with self._lock:
                volume.blocks = snapshot.blocks.copy()
                volume.used_bytes = snapshot.used_bytes

            return volume
        else:
            # Create new volume from snapshot
            volume = await self.create_volume(
                name=f"{snapshot.volume_name}-restored",
                size_gb=snapshot.size_bytes / (1024**3),
                description=f"Restored from snapshot {snapshot.name}",
            )

            # Copy block references
            async with self._lock:
                volume.blocks = snapshot.blocks.copy()
                volume.used_bytes = snapshot.used_bytes

            return volume

    # Statistics and Metrics

    async def get_stats(self) -> dict:
        """Get comprehensive storage statistics."""
        purity_stats = await self.purity.get_stats()
        flash_stats = await self.directflash.get_stats()
        volume_stats = await self.volume_manager.get_stats()
        snapshot_stats = await self.snapshot_manager.get_stats()

        return {
            "data_reduction": purity_stats,
            "flash": flash_stats,
            "volumes": volume_stats,
            "snapshots": snapshot_stats,
        }

    async def get_reduction_ratio(self) -> float:
        """Get current data reduction ratio."""
        return await self.purity.get_reduction_ratio()


# Singleton controller instance
_controller: StorageController | None = None


async def get_controller() -> StorageController:
    """Get or create the storage controller singleton."""
    global _controller
    if _controller is None:
        _controller = StorageController()
        await _controller.initialize()
    return _controller
