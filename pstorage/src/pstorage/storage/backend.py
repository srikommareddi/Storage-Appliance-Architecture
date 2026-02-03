"""Abstract storage backend interface."""

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import AsyncIterator


@dataclass
class BlockInfo:
    """Information about a stored block."""

    block_id: str
    offset: int
    size: int
    checksum: str
    compressed: bool
    deduplicated: bool
    reference_count: int


class StorageBackend(ABC):
    """Abstract base class for storage backends."""

    @abstractmethod
    async def initialize(self) -> None:
        """Initialize the storage backend."""
        pass

    @abstractmethod
    async def shutdown(self) -> None:
        """Shutdown the storage backend."""
        pass

    @abstractmethod
    async def write_block(self, block_id: str, data: bytes) -> BlockInfo:
        """
        Write a block of data to storage.

        Args:
            block_id: Unique identifier for the block
            data: Raw block data

        Returns:
            BlockInfo with metadata about the stored block
        """
        pass

    @abstractmethod
    async def read_block(self, block_id: str) -> bytes:
        """
        Read a block of data from storage.

        Args:
            block_id: Unique identifier for the block

        Returns:
            Raw block data

        Raises:
            KeyError: If block not found
        """
        pass

    @abstractmethod
    async def delete_block(self, block_id: str) -> bool:
        """
        Delete a block from storage.

        Args:
            block_id: Unique identifier for the block

        Returns:
            True if deleted, False if not found
        """
        pass

    @abstractmethod
    async def block_exists(self, block_id: str) -> bool:
        """Check if a block exists in storage."""
        pass

    @abstractmethod
    async def get_block_info(self, block_id: str) -> BlockInfo | None:
        """Get metadata for a block."""
        pass

    @abstractmethod
    async def list_blocks(self) -> AsyncIterator[str]:
        """List all block IDs in storage."""
        pass

    @abstractmethod
    async def get_stats(self) -> dict:
        """Get storage statistics."""
        pass
