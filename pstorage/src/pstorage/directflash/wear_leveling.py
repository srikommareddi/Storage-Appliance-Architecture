"""Wear Leveling - Distribute writes evenly across flash blocks."""

import asyncio
from dataclasses import dataclass
from enum import Enum

from .block_manager import BlockManager, BlockState


class WearLevelingStrategy(Enum):
    """Wear leveling strategies."""

    DYNAMIC = "dynamic"  # Only move hot data
    STATIC = "static"  # Also move cold data periodically


@dataclass
class WearLevelingStats:
    """Wear leveling statistics."""

    blocks_moved: int = 0
    hot_to_cold_moves: int = 0
    cold_to_hot_moves: int = 0
    wear_variance: float = 0.0  # Variance in erase counts


class WearLeveling:
    """
    Wear leveling algorithm.

    Ensures even distribution of program/erase cycles across all flash blocks
    to maximize flash lifespan.

    Two strategies:
    - Dynamic: Move hot (frequently written) data to less-worn blocks
    - Static: Also periodically move cold (rarely written) data
    """

    def __init__(
        self,
        block_manager: BlockManager,
        strategy: WearLevelingStrategy = WearLevelingStrategy.DYNAMIC,
        threshold: int = 1000,  # P/E count difference to trigger leveling
    ):
        self.block_manager = block_manager
        self.strategy = strategy
        self.threshold = threshold

        self._stats = WearLevelingStats()
        self._lock = asyncio.Lock()

        # Track write frequency per logical address
        self._write_counts: dict[int, int] = {}

    async def record_write(self, logical_address: int) -> None:
        """Record a write to track hot/cold data."""
        async with self._lock:
            self._write_counts[logical_address] = (
                self._write_counts.get(logical_address, 0) + 1
            )

    def is_hot_data(self, logical_address: int, threshold: int = 10) -> bool:
        """Check if data at address is hot (frequently written)."""
        return self._write_counts.get(logical_address, 0) > threshold

    async def should_level(self) -> bool:
        """Check if wear leveling is needed."""
        stats = await self.block_manager.get_stats()
        max_erase = stats["max_erase_count"]
        min_erase = stats["min_erase_count"]

        return (max_erase - min_erase) > self.threshold

    async def find_candidates(self) -> tuple[int | None, int | None]:
        """
        Find source and destination blocks for wear leveling.

        Returns:
            Tuple of (source_block_id, dest_block_id) or (None, None)
        """
        blocks = self.block_manager._blocks

        # Find most worn valid block (source - contains data to move)
        most_worn_valid = None
        max_erase = -1

        # Find least worn free block (destination)
        least_worn_free = None
        min_erase = float("inf")

        for block_id, block in blocks.items():
            if block.state == BlockState.VALID:
                if block.erase_count > max_erase:
                    max_erase = block.erase_count
                    most_worn_valid = block_id
            elif block.state == BlockState.FREE:
                if block.erase_count < min_erase:
                    min_erase = block.erase_count
                    least_worn_free = block_id

        # Only proceed if difference exceeds threshold
        if (
            most_worn_valid is not None
            and least_worn_free is not None
            and (max_erase - min_erase) > self.threshold
        ):
            return most_worn_valid, least_worn_free

        return None, None

    async def level_block(self, source_id: int, dest_id: int) -> bool:
        """
        Move data from source block to destination block.

        Args:
            source_id: Source physical block ID
            dest_id: Destination physical block ID

        Returns:
            True if leveling was successful
        """
        async with self._lock:
            source = self.block_manager._blocks.get(source_id)
            dest = self.block_manager._blocks.get(dest_id)

            if not source or not dest:
                return False

            if source.state != BlockState.VALID or dest.state != BlockState.FREE:
                return False

            # Copy data to destination
            dest.data = source.data
            dest.state = BlockState.VALID
            dest.logical_address = source.logical_address

            # Update L2P mapping
            if source.logical_address is not None:
                self.block_manager._l2p_table[source.logical_address] = dest_id

            # Invalidate source
            source.state = BlockState.INVALID
            source.logical_address = None

            # Remove destination from free pool
            self.block_manager._free_blocks.discard(dest_id)
            self.block_manager._stats.free_blocks -= 1
            self.block_manager._stats.invalid_blocks += 1

            self._stats.blocks_moved += 1

            return True

    async def run_leveling_pass(self) -> int:
        """
        Run one pass of wear leveling.

        Returns:
            Number of blocks moved
        """
        if not await self.should_level():
            return 0

        moved = 0
        while True:
            source_id, dest_id = await self.find_candidates()
            if source_id is None or dest_id is None:
                break

            if await self.level_block(source_id, dest_id):
                moved += 1

                # Erase the old source block to make it free
                await self.block_manager.erase_block(source_id)
            else:
                break

        return moved

    async def get_wear_distribution(self) -> dict[int, int]:
        """Get distribution of erase counts."""
        distribution: dict[int, int] = {}

        for block in self.block_manager._blocks.values():
            bucket = (block.erase_count // 100) * 100  # Bucket by 100s
            distribution[bucket] = distribution.get(bucket, 0) + 1

        return dict(sorted(distribution.items()))

    async def get_stats(self) -> dict:
        """Get wear leveling statistics."""
        distribution = await self.get_wear_distribution()
        erase_counts = [b.erase_count for b in self.block_manager._blocks.values()]

        # Calculate variance
        if erase_counts:
            mean = sum(erase_counts) / len(erase_counts)
            variance = sum((x - mean) ** 2 for x in erase_counts) / len(erase_counts)
        else:
            variance = 0.0

        return {
            "blocks_moved": self._stats.blocks_moved,
            "strategy": self.strategy.value,
            "threshold": self.threshold,
            "wear_variance": variance,
            "wear_distribution": distribution,
        }
