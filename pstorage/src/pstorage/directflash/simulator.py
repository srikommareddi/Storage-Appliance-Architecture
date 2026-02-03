"""DirectFlash Simulator - Main flash management interface."""

import asyncio
from dataclasses import dataclass

from ..core.config import Settings, get_settings
from .block_manager import BlockManager
from .garbage_collection import GarbageCollector
from .wear_leveling import WearLeveling, WearLevelingStrategy


@dataclass
class FlashStats:
    """Combined flash statistics."""

    total_capacity_bytes: int
    used_bytes: int
    free_bytes: int
    utilization: float
    health_percentage: float  # Based on wear


class DirectFlashSimulator:
    """
    DirectFlash Simulator.

    Simulates Pure Storage's DirectFlash architecture:
    - Direct access to raw NAND flash (bypassing SSD controller)
    - Global wear leveling across all flash
    - Efficient garbage collection
    - Predictable latency

    This is a software simulation for educational/development purposes.
    """

    def __init__(self, settings: Settings | None = None):
        self.settings = settings or get_settings()

        # Initialize block manager
        self.block_manager = BlockManager(
            total_blocks=self.settings.total_flash_blocks,
            block_size=self.settings.flash_block_size,
            page_size=self.settings.flash_page_size,
        )

        # Initialize wear leveling
        self.wear_leveling = WearLeveling(
            block_manager=self.block_manager,
            strategy=WearLevelingStrategy.DYNAMIC,
            threshold=self.settings.wear_leveling_threshold,
        )

        # Initialize garbage collector
        self.garbage_collector = GarbageCollector(
            block_manager=self.block_manager,
            gc_threshold=self.settings.gc_threshold,
            gc_target=self.settings.gc_target,
        )

        self._initialized = False
        self._lock = asyncio.Lock()

        # Logical address counter
        self._next_logical_address = 0

    async def initialize(self) -> None:
        """Initialize the DirectFlash simulator."""
        if self._initialized:
            return

        # Start background GC
        await self.garbage_collector.start_background_gc()

        self._initialized = True

    async def shutdown(self) -> None:
        """Shutdown the DirectFlash simulator."""
        await self.garbage_collector.stop_background_gc()
        self._initialized = False

    async def write(self, data: bytes) -> int:
        """
        Write data to flash.

        Args:
            data: Data to write

        Returns:
            Logical address of the written data
        """
        async with self._lock:
            logical_address = self._next_logical_address
            self._next_logical_address += 1

        physical_id, success = await self.block_manager.write_block(
            logical_address, data
        )

        if not success:
            # Try garbage collection and retry
            await self.garbage_collector.trigger_gc()
            physical_id, success = await self.block_manager.write_block(
                logical_address, data
            )

            if not success:
                raise IOError("Failed to write data: no free blocks")

        # Record for wear leveling
        await self.wear_leveling.record_write(logical_address)

        # Periodically run wear leveling
        if await self.wear_leveling.should_level():
            await self.wear_leveling.run_leveling_pass()

        return logical_address

    async def read(self, logical_address: int) -> bytes | None:
        """
        Read data from flash.

        Args:
            logical_address: Logical address to read from

        Returns:
            Data or None if not found
        """
        return await self.block_manager.read_block(logical_address)

    async def delete(self, logical_address: int) -> bool:
        """
        Delete data from flash.

        Args:
            logical_address: Logical address to delete

        Returns:
            True if deleted
        """
        return await self.block_manager.delete_block(logical_address)

    async def trim(self, logical_address: int) -> bool:
        """
        TRIM command - mark block as no longer needed.

        Similar to delete but commonly used terminology in flash storage.
        """
        return await self.delete(logical_address)

    async def get_capacity(self) -> tuple[int, int]:
        """
        Get capacity information.

        Returns:
            Tuple of (total_bytes, free_bytes)
        """
        stats = await self.block_manager.get_stats()
        total_bytes = stats["total_blocks"] * self.block_manager.block_size
        free_bytes = stats["free_blocks"] * self.block_manager.block_size
        return total_bytes, free_bytes

    async def get_health(self) -> float:
        """
        Get flash health percentage based on wear.

        Returns:
            Health percentage (100% = new, 0% = worn out)
        """
        stats = await self.block_manager.get_stats()
        avg_erase = stats["avg_erase_count"]
        max_endurance = self.block_manager.max_erase_count

        remaining = max(0, max_endurance - avg_erase)
        return (remaining / max_endurance) * 100

    async def get_stats(self) -> dict:
        """Get comprehensive flash statistics."""
        block_stats = await self.block_manager.get_stats()
        wear_stats = await self.wear_leveling.get_stats()
        gc_stats = await self.garbage_collector.get_stats()

        total_bytes, free_bytes = await self.get_capacity()
        health = await self.get_health()

        return {
            "flash": {
                "total_capacity_bytes": total_bytes,
                "used_bytes": total_bytes - free_bytes,
                "free_bytes": free_bytes,
                "utilization": block_stats["utilization"],
                "health_percentage": health,
            },
            "blocks": block_stats,
            "wear_leveling": wear_stats,
            "garbage_collection": gc_stats,
        }

    async def force_gc(self) -> int:
        """Force garbage collection."""
        return await self.garbage_collector.trigger_gc()

    async def force_wear_leveling(self) -> int:
        """Force wear leveling pass."""
        return await self.wear_leveling.run_leveling_pass()
