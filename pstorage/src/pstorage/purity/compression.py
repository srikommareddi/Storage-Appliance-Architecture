"""Compression Engine - LZ4 and Zstd compression support."""

import threading
from dataclasses import dataclass
from enum import Enum
from typing import Literal

import lz4.frame
import zstandard as zstd


class CompressionAlgorithm(Enum):
    """Supported compression algorithms."""

    NONE = "none"
    LZ4 = "lz4"
    ZSTD = "zstd"


@dataclass
class CompressionResult:
    """Result of compression operation."""

    original_size: int
    compressed_size: int
    algorithm: CompressionAlgorithm
    compression_ratio: float
    was_compressed: bool  # False if compression made data larger


# Header format: 1 byte algorithm + 4 bytes original size
HEADER_SIZE = 5
ALGORITHM_BYTES = {
    CompressionAlgorithm.NONE: b"\x00",
    CompressionAlgorithm.LZ4: b"\x01",
    CompressionAlgorithm.ZSTD: b"\x02",
}
BYTES_TO_ALGORITHM = {v: k for k, v in ALGORITHM_BYTES.items()}


class CompressionEngine:
    """
    Compression engine supporting LZ4 and Zstd.

    - LZ4: Fast compression for inline use (default)
    - Zstd: Better ratio for post-process deep compression
    """

    def __init__(
        self,
        algorithm: Literal["lz4", "zstd"] = "lz4",
        level: int = 1,
        min_compression_ratio: float = 1.1,  # Only keep if 10%+ smaller
    ):
        self.algorithm = CompressionAlgorithm(algorithm)
        self.level = level
        self.min_compression_ratio = min_compression_ratio

        # Initialize Zstd compressor/decompressor
        self._zstd_compressor = zstd.ZstdCompressor(level=level)
        self._zstd_decompressor = zstd.ZstdDecompressor()

        self._stats_lock = threading.Lock()
        self._stats = {
            "blocks_compressed": 0,
            "blocks_skipped": 0,
            "bytes_before": 0,
            "bytes_after": 0,
        }

    def compress(self, data: bytes) -> tuple[bytes, CompressionResult]:
        """
        Compress data using configured algorithm.

        Args:
            data: Data to compress

        Returns:
            Tuple of (compressed_data_with_header, CompressionResult)
        """
        original_size = len(data)
        with self._stats_lock:
            self._stats["bytes_before"] += original_size

        if self.algorithm == CompressionAlgorithm.NONE:
            result = CompressionResult(
                original_size=original_size,
                compressed_size=original_size,
                algorithm=CompressionAlgorithm.NONE,
                compression_ratio=1.0,
                was_compressed=False,
            )
            return data, result

        # Compress based on algorithm
        if self.algorithm == CompressionAlgorithm.LZ4:
            compressed = lz4.frame.compress(data)
        else:  # ZSTD
            compressed = self._zstd_compressor.compress(data)

        compressed_size = len(compressed)
        ratio = original_size / compressed_size if compressed_size > 0 else 1.0

        # Check if compression is beneficial
        if ratio < self.min_compression_ratio:
            # Not worth compressing
            with self._stats_lock:
                self._stats["blocks_skipped"] += 1
                self._stats["bytes_after"] += original_size
            result = CompressionResult(
                original_size=original_size,
                compressed_size=original_size,
                algorithm=CompressionAlgorithm.NONE,
                compression_ratio=1.0,
                was_compressed=False,
            )
            # Return with NONE header
            header = ALGORITHM_BYTES[CompressionAlgorithm.NONE] + original_size.to_bytes(
                4, "big"
            )
            return header + data, result

        # Compression is beneficial
        with self._stats_lock:
            self._stats["blocks_compressed"] += 1
            self._stats["bytes_after"] += compressed_size

        result = CompressionResult(
            original_size=original_size,
            compressed_size=compressed_size,
            algorithm=self.algorithm,
            compression_ratio=ratio,
            was_compressed=True,
        )

        # Add header
        header = ALGORITHM_BYTES[self.algorithm] + original_size.to_bytes(4, "big")
        return header + compressed, result

    def decompress(self, data: bytes) -> bytes:
        """
        Decompress data, handling header and algorithm detection.

        Args:
            data: Compressed data with header

        Returns:
            Original uncompressed data
        """
        if len(data) < HEADER_SIZE:
            # No header, return as-is
            return data

        # Parse header
        algo_byte = data[0:1]
        original_size = int.from_bytes(data[1:5], "big")
        payload = data[5:]

        algorithm = BYTES_TO_ALGORITHM.get(algo_byte, CompressionAlgorithm.NONE)

        if algorithm == CompressionAlgorithm.NONE:
            return payload

        if algorithm == CompressionAlgorithm.LZ4:
            return lz4.frame.decompress(payload)

        if algorithm == CompressionAlgorithm.ZSTD:
            return self._zstd_decompressor.decompress(payload)

        return payload

    def is_compressed(self, data: bytes) -> bool:
        """Check if data has compression header and is compressed."""
        if len(data) < HEADER_SIZE:
            return False
        algo_byte = data[0:1]
        algorithm = BYTES_TO_ALGORITHM.get(algo_byte, CompressionAlgorithm.NONE)
        return algorithm != CompressionAlgorithm.NONE

    def has_header(self, data: bytes) -> bool:
        """Check if data has any compression header (including NONE)."""
        if len(data) < HEADER_SIZE:
            return False
        algo_byte = data[0:1]
        return algo_byte in BYTES_TO_ALGORITHM

    def get_compression_ratio(self) -> float:
        """Get overall compression ratio."""
        with self._stats_lock:
            if self._stats["bytes_after"] == 0:
                return 1.0
            return self._stats["bytes_before"] / self._stats["bytes_after"]

    def get_stats(self) -> dict:
        """Get compression statistics."""
        with self._stats_lock:
            # Calculate ratio inline to avoid nested lock
            ratio = (
                self._stats["bytes_before"] / self._stats["bytes_after"]
                if self._stats["bytes_after"] > 0
                else 1.0
            )
            return {
                **self._stats,
                "compression_ratio": ratio,
                "algorithm": self.algorithm.value,
            }


class DeepCompressionEngine(CompressionEngine):
    """
    Deep compression engine using Zstd with higher compression levels.

    Used for post-process compression to achieve better ratios
    at the cost of CPU time.
    """

    def __init__(self, level: int = 9):
        super().__init__(algorithm="zstd", level=level, min_compression_ratio=1.05)
