"""Purity Engine - Main data reduction orchestrator."""

import asyncio
import uuid
from dataclasses import dataclass
from typing import Literal

from ..core.config import Settings, get_settings
from ..storage.backend import StorageBackend
from .compression import CompressionEngine, CompressionResult
from .deduplication import DeduplicationEngine, DedupResult
from .pattern_removal import PatternRemoval, PatternResult


@dataclass
class ReductionResult:
    """Result of full data reduction pipeline."""

    block_id: str
    original_size: int
    stored_size: int
    reduction_ratio: float

    # Stage results
    pattern_result: PatternResult
    dedupe_result: DedupResult
    compression_result: CompressionResult | None

    # Flags
    was_pattern_reduced: bool
    was_deduplicated: bool
    was_compressed: bool


@dataclass
class ReductionStats:
    """Overall data reduction statistics."""

    total_bytes_in: int = 0
    total_bytes_stored: int = 0
    pattern_bytes_saved: int = 0
    dedupe_bytes_saved: int = 0
    compression_bytes_saved: int = 0

    @property
    def total_reduction_ratio(self) -> float:
        if self.total_bytes_stored == 0:
            return 1.0
        return self.total_bytes_in / self.total_bytes_stored


class PurityEngine:
    """
    Purity Data Reduction Engine.

    Implements Pure Storage-inspired data reduction pipeline:
    1. Pattern Removal - Detect zero blocks and repeated patterns
    2. Deduplication - Hash-based block deduplication
    3. Compression - LZ4/Zstd compression

    All reduction is "always on" with no performance impact,
    following Pure Storage's philosophy.
    """

    def __init__(
        self,
        backend: StorageBackend,
        settings: Settings | None = None,
    ):
        self.settings = settings or get_settings()
        self.backend = backend

        # Initialize reduction components
        self.pattern_removal = PatternRemoval(
            min_block_size=self.settings.default_block_size
        )

        self.deduplication = DeduplicationEngine(
            min_block_size=self.settings.dedupe_block_size_min,
            max_block_size=self.settings.dedupe_block_size_max,
        )

        self.compression = CompressionEngine(
            algorithm=self.settings.compression_algorithm,
            level=self.settings.compression_level,
        )

        self._stats = ReductionStats()
        self._lock = asyncio.Lock()

    async def reduce_and_store(self, data: bytes) -> ReductionResult:
        """
        Apply full data reduction pipeline and store.

        Pipeline:
        1. Pattern removal (zero/pattern detection)
        2. Deduplication (if not pattern-reduced)
        3. Compression (if not deduplicated)

        Args:
            data: Raw data block to reduce and store

        Returns:
            ReductionResult with details of all reduction stages
        """
        original_size = len(data)
        self._stats.total_bytes_in += original_size

        # Stage 1: Pattern Removal
        pattern_result = self.pattern_removal.analyze(data)
        was_pattern_reduced = False
        current_data = data

        if self.settings.pattern_removal_enabled:
            if pattern_result.is_zero_block or pattern_result.is_repeated_pattern:
                current_data, was_pattern_reduced = self.pattern_removal.encode(data)
                if was_pattern_reduced:
                    self._stats.pattern_bytes_saved += original_size - len(current_data)

        # Stage 2: Deduplication
        dedupe_result = await self.deduplication.check_duplicate(current_data)
        was_deduplicated = False
        block_id: str

        if self.settings.dedupe_enabled and dedupe_result.is_duplicate:
            # Data already exists - just reference it
            was_deduplicated = True
            block_id = dedupe_result.existing_block_id  # type: ignore
            await self.deduplication.register_block(current_data, block_id)
            self._stats.dedupe_bytes_saved += len(current_data)

            stored_size = 0  # No new storage needed
            compression_result = None
        else:
            # Stage 3: Compression (only for new unique blocks)
            compression_result = None
            was_compressed = False

            if self.settings.compression_enabled and not was_pattern_reduced:
                current_data, compression_result = self.compression.compress(
                    current_data
                )
                was_compressed = compression_result.was_compressed
                if was_compressed:
                    self._stats.compression_bytes_saved += (
                        compression_result.original_size
                        - compression_result.compressed_size
                    )

            # Generate block ID and store
            block_id = str(uuid.uuid4())
            await self.backend.write_block(block_id, current_data)
            await self.deduplication.register_block(data, block_id)

            stored_size = len(current_data)

        self._stats.total_bytes_stored += stored_size if not was_deduplicated else 0

        # Calculate reduction ratio (use large value for fully deduplicated blocks)
        effective_stored = stored_size if not was_deduplicated else 0
        if effective_stored > 0:
            reduction_ratio = original_size / effective_stored
        else:
            # Fully deduplicated - use original_size as ratio (represents 100% savings)
            reduction_ratio = float(original_size) if original_size > 0 else 1.0

        return ReductionResult(
            block_id=block_id,
            original_size=original_size,
            stored_size=stored_size,
            reduction_ratio=reduction_ratio,
            pattern_result=pattern_result,
            dedupe_result=dedupe_result,
            compression_result=compression_result,
            was_pattern_reduced=was_pattern_reduced,
            was_deduplicated=was_deduplicated,
            was_compressed=compression_result.was_compressed
            if compression_result
            else False,
        )

    async def retrieve(self, block_id: str, fingerprint: str | None = None) -> bytes:
        """
        Retrieve and decompress/decode data.

        Args:
            block_id: ID of the stored block
            fingerprint: Optional fingerprint for dedup lookup

        Returns:
            Original uncompressed data
        """
        # Read from storage
        data = await self.backend.read_block(block_id)

        # Decompress if needed (has_header checks for any compression header including NONE)
        if self.compression.has_header(data):
            data = self.compression.decompress(data)

        # Decode patterns if needed
        if self.pattern_removal.is_encoded(data):
            data = self.pattern_removal.decode(data)

        return data

    async def delete(self, block_id: str, fingerprint: str) -> bool:
        """
        Delete a block (with reference counting).

        Args:
            block_id: ID of the block
            fingerprint: Block fingerprint for dedup tracking

        Returns:
            True if block was actually deleted (ref count reached 0)
        """
        ref_count = await self.deduplication.dereference_block(fingerprint)

        if ref_count == 0:
            # Actually delete the block
            return await self.backend.delete_block(block_id)

        return False

    async def get_stats(self) -> dict:
        """Get comprehensive reduction statistics."""
        pattern_stats = self.pattern_removal.get_stats()
        dedupe_stats = await self.deduplication.get_stats()
        compression_stats = self.compression.get_stats()
        backend_stats = await self.backend.get_stats()

        return {
            "overall": {
                "total_bytes_in": self._stats.total_bytes_in,
                "total_bytes_stored": self._stats.total_bytes_stored,
                "total_reduction_ratio": self._stats.total_reduction_ratio,
                "pattern_bytes_saved": self._stats.pattern_bytes_saved,
                "dedupe_bytes_saved": self._stats.dedupe_bytes_saved,
                "compression_bytes_saved": self._stats.compression_bytes_saved,
            },
            "pattern_removal": pattern_stats,
            "deduplication": dedupe_stats,
            "compression": compression_stats,
            "backend": backend_stats,
        }

    async def get_reduction_ratio(self) -> float:
        """Get current overall data reduction ratio."""
        return self._stats.total_reduction_ratio
