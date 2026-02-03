"""Block Manager - Logical to physical block mapping and allocation."""

import asyncio
from dataclasses import dataclass, field
from enum import Enum
from typing import Iterator


class BlockState(Enum):
    """State of a flash block."""

    FREE = "free"
    VALID = "valid"
    INVALID = "invalid"  # Contains stale data, can be erased
    BAD = "bad"  # Failed block, unusable


@dataclass
class PhysicalBlock:
    """Represents a physical flash block."""

    block_id: int
    state: BlockState = BlockState.FREE
    erase_count: int = 0
    data: bytes = b""
    logical_address: int | None = None  # Mapped logical address

    @property
    def is_free(self) -> bool:
        return self.state == BlockState.FREE

    @property
    def is_valid(self) -> bool:
        return self.state == BlockState.VALID

    @property
    def is_invalid(self) -> bool:
        return self.state == BlockState.INVALID


@dataclass
class BlockManagerStats:
    """Block manager statistics."""

    total_blocks: int = 0
    free_blocks: int = 0
    valid_blocks: int = 0
    invalid_blocks: int = 0
    bad_blocks: int = 0
    total_writes: int = 0
    total_reads: int = 0
    total_erases: int = 0


class BlockManager:
    """
    Manages logical-to-physical block mapping.

    Simulates flash memory characteristics:
    - Pages can only be written once before erase
    - Erase is done at block granularity
    - Blocks wear out after many P/E cycles
    """

    def __init__(
        self,
        total_blocks: int = 1024,
        block_size: int = 262144,  # 256KB
        page_size: int = 4096,  # 4KB
        max_erase_count: int = 100000,  # Flash endurance
    ):
        self.total_blocks = total_blocks
        self.block_size = block_size
        self.page_size = page_size
        self.pages_per_block = block_size // page_size
        self.max_erase_count = max_erase_count

        # Physical blocks storage
        self._blocks: dict[int, PhysicalBlock] = {
            i: PhysicalBlock(block_id=i) for i in range(total_blocks)
        }

        # Logical to physical mapping (L2P table)
        self._l2p_table: dict[int, int] = {}

        # Free block pool
        self._free_blocks: set[int] = set(range(total_blocks))

        self._lock = asyncio.Lock()
        self._stats = BlockManagerStats(
            total_blocks=total_blocks, free_blocks=total_blocks
        )

    async def allocate_block(self) -> int | None:
        """
        Allocate a free physical block.

        Returns:
            Physical block ID or None if no free blocks
        """
        async with self._lock:
            if not self._free_blocks:
                return None

            # Get block with lowest erase count (simple wear leveling)
            block_id = min(
                self._free_blocks, key=lambda b: self._blocks[b].erase_count
            )

            self._free_blocks.remove(block_id)
            self._stats.free_blocks -= 1

            return block_id

    async def write_block(
        self, logical_address: int, data: bytes
    ) -> tuple[int, bool]:
        """
        Write data to a logical address.

        Args:
            logical_address: Logical block address
            data: Data to write (must fit in block)

        Returns:
            Tuple of (physical_block_id, success)
        """
        if len(data) > self.block_size:
            raise ValueError(f"Data too large: {len(data)} > {self.block_size}")

        async with self._lock:
            # Check if logical address already mapped
            if logical_address in self._l2p_table:
                # Invalidate old physical block
                old_physical = self._l2p_table[logical_address]
                self._blocks[old_physical].state = BlockState.INVALID
                self._blocks[old_physical].logical_address = None
                self._stats.valid_blocks -= 1
                self._stats.invalid_blocks += 1

        # Allocate new physical block
        physical_id = await self.allocate_block()
        if physical_id is None:
            return -1, False

        async with self._lock:
            # Write to physical block
            block = self._blocks[physical_id]
            block.data = data
            block.state = BlockState.VALID
            block.logical_address = logical_address

            # Update L2P mapping
            self._l2p_table[logical_address] = physical_id

            self._stats.valid_blocks += 1
            self._stats.total_writes += 1

            return physical_id, True

    async def read_block(self, logical_address: int) -> bytes | None:
        """
        Read data from a logical address.

        Args:
            logical_address: Logical block address

        Returns:
            Data or None if not found
        """
        async with self._lock:
            if logical_address not in self._l2p_table:
                return None

            physical_id = self._l2p_table[logical_address]
            block = self._blocks[physical_id]

            if block.state != BlockState.VALID:
                return None

            self._stats.total_reads += 1
            return block.data

    async def delete_block(self, logical_address: int) -> bool:
        """
        Delete/invalidate a logical block.

        Args:
            logical_address: Logical block address

        Returns:
            True if deleted, False if not found
        """
        async with self._lock:
            if logical_address not in self._l2p_table:
                return False

            physical_id = self._l2p_table[logical_address]
            block = self._blocks[physical_id]

            block.state = BlockState.INVALID
            block.logical_address = None
            del self._l2p_table[logical_address]

            self._stats.valid_blocks -= 1
            self._stats.invalid_blocks += 1

            return True

    async def erase_block(self, physical_id: int) -> bool:
        """
        Erase a physical block (makes it free again).

        Args:
            physical_id: Physical block ID

        Returns:
            True if erased, False if block is bad
        """
        async with self._lock:
            if physical_id not in self._blocks:
                return False

            block = self._blocks[physical_id]

            if block.state == BlockState.BAD:
                return False

            # Increment erase count
            block.erase_count += 1

            # Check if block has worn out
            if block.erase_count >= self.max_erase_count:
                block.state = BlockState.BAD
                self._stats.bad_blocks += 1
                if block.state == BlockState.INVALID:
                    self._stats.invalid_blocks -= 1
                return False

            # Reset block
            block.data = b""
            block.state = BlockState.FREE
            block.logical_address = None

            if block.state == BlockState.INVALID:
                self._stats.invalid_blocks -= 1
            self._stats.free_blocks += 1
            self._stats.total_erases += 1

            self._free_blocks.add(physical_id)

            return True

    def get_invalid_blocks(self) -> Iterator[int]:
        """Get all invalid (garbage) blocks."""
        for block_id, block in self._blocks.items():
            if block.state == BlockState.INVALID:
                yield block_id

    def get_block_info(self, physical_id: int) -> PhysicalBlock | None:
        """Get information about a physical block."""
        return self._blocks.get(physical_id)

    async def get_stats(self) -> dict:
        """Get block manager statistics."""
        async with self._lock:
            avg_erase = (
                sum(b.erase_count for b in self._blocks.values()) / self.total_blocks
            )
            max_erase = max(b.erase_count for b in self._blocks.values())
            min_erase = min(
                b.erase_count for b in self._blocks.values() if b.state != BlockState.BAD
            )

            return {
                "total_blocks": self._stats.total_blocks,
                "free_blocks": self._stats.free_blocks,
                "valid_blocks": self._stats.valid_blocks,
                "invalid_blocks": self._stats.invalid_blocks,
                "bad_blocks": self._stats.bad_blocks,
                "total_writes": self._stats.total_writes,
                "total_reads": self._stats.total_reads,
                "total_erases": self._stats.total_erases,
                "avg_erase_count": avg_erase,
                "max_erase_count": max_erase,
                "min_erase_count": min_erase,
                "utilization": self._stats.valid_blocks / self._stats.total_blocks,
            }
