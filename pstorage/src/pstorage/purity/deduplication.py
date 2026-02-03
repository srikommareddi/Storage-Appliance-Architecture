"""Deduplication Engine - hash-based block deduplication."""

import asyncio
import hashlib
from dataclasses import dataclass, field
from typing import Iterator


@dataclass
class DedupEntry:
    """Entry in the deduplication table."""

    fingerprint: str  # SHA-256 hash
    block_id: str  # Reference to actual stored block
    size: int  # Original block size
    reference_count: int = 1


@dataclass
class DedupResult:
    """Result of deduplication operation."""

    is_duplicate: bool
    fingerprint: str
    existing_block_id: str | None  # If duplicate, the existing block
    reference_count: int


@dataclass
class DedupStats:
    """Deduplication statistics."""

    total_blocks: int = 0
    unique_blocks: int = 0
    duplicate_blocks: int = 0
    bytes_before_dedupe: int = 0
    bytes_after_dedupe: int = 0

    @property
    def dedupe_ratio(self) -> float:
        """Calculate deduplication ratio."""
        if self.bytes_after_dedupe == 0:
            return 1.0
        return self.bytes_before_dedupe / self.bytes_after_dedupe


class DeduplicationEngine:
    """
    Hash-based deduplication engine.

    Inspired by Pure Storage's variable-block deduplication with
    SHA-256 fingerprinting and reference counting.
    """

    def __init__(
        self,
        min_block_size: int = 4096,
        max_block_size: int = 32768,
    ):
        self.min_block_size = min_block_size
        self.max_block_size = max_block_size

        # Fingerprint -> DedupEntry mapping
        self._dedupe_table: dict[str, DedupEntry] = {}

        # Lock for thread-safe operations
        self._lock = asyncio.Lock()

        self._stats = DedupStats()

    def compute_fingerprint(self, data: bytes) -> str:
        """
        Compute SHA-256 fingerprint for data block.

        Args:
            data: Block data

        Returns:
            Hex-encoded SHA-256 hash
        """
        return hashlib.sha256(data).hexdigest()

    async def check_duplicate(self, data: bytes) -> DedupResult:
        """
        Check if data block is a duplicate.

        Args:
            data: Block data to check

        Returns:
            DedupResult indicating if duplicate and reference info
        """
        fingerprint = self.compute_fingerprint(data)

        async with self._lock:
            if fingerprint in self._dedupe_table:
                entry = self._dedupe_table[fingerprint]
                return DedupResult(
                    is_duplicate=True,
                    fingerprint=fingerprint,
                    existing_block_id=entry.block_id,
                    reference_count=entry.reference_count,
                )

            return DedupResult(
                is_duplicate=False,
                fingerprint=fingerprint,
                existing_block_id=None,
                reference_count=0,
            )

    async def register_block(
        self, data: bytes, block_id: str
    ) -> tuple[str, bool]:
        """
        Register a block in the deduplication table.

        Args:
            data: Block data
            block_id: ID of the stored block

        Returns:
            Tuple of (fingerprint, is_new_block)
        """
        fingerprint = self.compute_fingerprint(data)
        size = len(data)

        async with self._lock:
            self._stats.total_blocks += 1
            self._stats.bytes_before_dedupe += size

            if fingerprint in self._dedupe_table:
                # Duplicate - increment reference count
                self._dedupe_table[fingerprint].reference_count += 1
                self._stats.duplicate_blocks += 1
                return fingerprint, False

            # New unique block
            self._dedupe_table[fingerprint] = DedupEntry(
                fingerprint=fingerprint,
                block_id=block_id,
                size=size,
                reference_count=1,
            )
            self._stats.unique_blocks += 1
            self._stats.bytes_after_dedupe += size
            return fingerprint, True

    async def dereference_block(self, fingerprint: str) -> int:
        """
        Decrement reference count for a block.

        Args:
            fingerprint: Block fingerprint

        Returns:
            New reference count (0 means block can be deleted)
        """
        async with self._lock:
            if fingerprint not in self._dedupe_table:
                return 0

            entry = self._dedupe_table[fingerprint]
            entry.reference_count -= 1

            if entry.reference_count <= 0:
                # Remove from table
                self._stats.bytes_after_dedupe -= entry.size
                del self._dedupe_table[fingerprint]
                return 0

            return entry.reference_count

    async def get_block_id(self, fingerprint: str) -> str | None:
        """Get the block ID for a fingerprint."""
        async with self._lock:
            entry = self._dedupe_table.get(fingerprint)
            return entry.block_id if entry else None

    async def get_entry(self, fingerprint: str) -> DedupEntry | None:
        """Get the full dedup entry for a fingerprint."""
        async with self._lock:
            return self._dedupe_table.get(fingerprint)

    def chunk_data(self, data: bytes) -> Iterator[tuple[int, bytes]]:
        """
        Chunk data into variable-size blocks for deduplication.

        Uses content-defined chunking with Rabin fingerprinting concept
        (simplified implementation using fixed boundaries for demo).

        Args:
            data: Data to chunk

        Yields:
            Tuples of (offset, chunk_data)
        """
        offset = 0
        while offset < len(data):
            # Determine chunk size (simplified: use min_block_size)
            # In production, would use content-defined chunking
            chunk_size = min(self.min_block_size, len(data) - offset)
            yield offset, data[offset : offset + chunk_size]
            offset += chunk_size

    async def get_stats(self) -> dict:
        """Get deduplication statistics."""
        async with self._lock:
            return {
                "total_blocks": self._stats.total_blocks,
                "unique_blocks": self._stats.unique_blocks,
                "duplicate_blocks": self._stats.duplicate_blocks,
                "bytes_before_dedupe": self._stats.bytes_before_dedupe,
                "bytes_after_dedupe": self._stats.bytes_after_dedupe,
                "dedupe_ratio": self._stats.dedupe_ratio,
                "table_size": len(self._dedupe_table),
            }

    async def clear(self) -> None:
        """Clear the deduplication table."""
        async with self._lock:
            self._dedupe_table.clear()
            self._stats = DedupStats()
