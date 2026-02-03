"""Garbage Collection - Reclaim invalid flash blocks."""

import asyncio
from dataclasses import dataclass
from enum import Enum

from .block_manager import BlockManager, BlockState


class GCState(Enum):
    """Garbage collector state."""

    IDLE = "idle"
    RUNNING = "running"
    STOPPED = "stopped"


@dataclass
class GCStats:
    """Garbage collection statistics."""

    gc_runs: int = 0
    blocks_reclaimed: int = 0
    pages_migrated: int = 0
    bytes_reclaimed: int = 0


class GarbageCollector:
    """
    Garbage Collector for flash storage.

    Reclaims invalid blocks by:
    1. Finding blocks with invalid pages
    2. Migrating valid pages to new blocks
    3. Erasing the old blocks

    Runs in background when utilization exceeds threshold.
    """

    def __init__(
        self,
        block_manager: BlockManager,
        gc_threshold: float = 0.75,  # Start GC when 75% full
        gc_target: float = 0.50,  # GC until 50% free
    ):
        self.block_manager = block_manager
        self.gc_threshold = gc_threshold
        self.gc_target = gc_target

        self._state = GCState.IDLE
        self._stats = GCStats()
        self._lock = asyncio.Lock()
        self._gc_task: asyncio.Task | None = None

    @property
    def state(self) -> GCState:
        # Note: This is a quick read, but for accurate state, use get_state()
        return self._state

    async def get_state(self) -> GCState:
        """Get current GC state (thread-safe)."""
        async with self._lock:
            return self._state

    async def get_utilization(self) -> float:
        """Get current storage utilization."""
        stats = await self.block_manager.get_stats()
        total = stats["total_blocks"]
        free = stats["free_blocks"]
        return (total - free) / total if total > 0 else 0.0

    async def should_run(self) -> bool:
        """Check if GC should run based on utilization."""
        utilization = await self.get_utilization()
        return utilization >= self.gc_threshold

    async def run_gc_cycle(self) -> int:
        """
        Run one garbage collection cycle.

        Returns:
            Number of blocks reclaimed
        """
        async with self._lock:
            if self._state == GCState.STOPPED:
                return 0
            self._state = GCState.RUNNING

        reclaimed = 0

        # Find and erase all invalid blocks (now async)
        invalid_blocks = await self.block_manager.get_invalid_blocks()

        for block_id in invalid_blocks:
            # Check state under lock
            async with self._lock:
                if self._state == GCState.STOPPED:
                    break

            block = await self.block_manager.get_block_info(block_id)
            if block and block.state == BlockState.INVALID:
                if await self.block_manager.erase_block(block_id):
                    reclaimed += 1
                    async with self._lock:
                        self._stats.blocks_reclaimed += 1
                        self._stats.bytes_reclaimed += self.block_manager.block_size

            # Check if we've reached target
            utilization = await self.get_utilization()
            if utilization <= self.gc_target:
                break

        async with self._lock:
            self._stats.gc_runs += 1
            if self._state != GCState.STOPPED:
                self._state = GCState.IDLE

        return reclaimed

    async def start_background_gc(self, interval: float = 5.0) -> None:
        """
        Start background garbage collection.

        Args:
            interval: Check interval in seconds
        """
        async with self._lock:
            if self._gc_task is not None:
                return
            self._state = GCState.IDLE

        async def gc_loop():
            while True:
                async with self._lock:
                    if self._state == GCState.STOPPED:
                        break
                try:
                    if await self.should_run():
                        await self.run_gc_cycle()
                    await asyncio.sleep(interval)
                except asyncio.CancelledError:
                    break

        self._gc_task = asyncio.create_task(gc_loop())

    async def stop_background_gc(self) -> None:
        """Stop background garbage collection."""
        async with self._lock:
            self._state = GCState.STOPPED
            task = self._gc_task
            self._gc_task = None

        if task is not None:
            task.cancel()
            try:
                await task
            except asyncio.CancelledError:
                pass

    async def trigger_gc(self) -> int:
        """Manually trigger garbage collection."""
        return await self.run_gc_cycle()

    async def get_stats(self) -> dict:
        """Get garbage collection statistics."""
        utilization = await self.get_utilization()

        async with self._lock:
            return {
                "state": self._state.value,
                "gc_runs": self._stats.gc_runs,
                "blocks_reclaimed": self._stats.blocks_reclaimed,
                "bytes_reclaimed": self._stats.bytes_reclaimed,
                "current_utilization": utilization,
                "gc_threshold": self.gc_threshold,
                "gc_target": self.gc_target,
            }
