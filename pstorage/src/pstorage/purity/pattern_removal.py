"""Pattern Removal - detects and eliminates zero blocks and repeated patterns."""

from dataclasses import dataclass
from typing import Iterator


@dataclass
class PatternResult:
    """Result of pattern analysis."""

    is_zero_block: bool
    is_repeated_pattern: bool
    pattern_byte: int | None  # The repeated byte if is_repeated_pattern
    unique_bytes_ratio: float  # Ratio of unique bytes (0.0 = all same, 1.0 = all different)


class PatternRemoval:
    """
    Pattern removal engine - first stage of data reduction.

    Identifies and handles:
    - Zero blocks (all zeros)
    - Repeated pattern blocks (same byte repeated)
    - Low-entropy data suitable for special handling
    """

    # Special marker for zero blocks (stored instead of actual data)
    ZERO_BLOCK_MARKER = b"__ZERO_BLOCK__"
    PATTERN_BLOCK_PREFIX = b"__PATTERN:"  # Followed by single byte

    def __init__(self, min_block_size: int = 4096):
        self.min_block_size = min_block_size
        self._stats = {
            "blocks_analyzed": 0,
            "zero_blocks_found": 0,
            "pattern_blocks_found": 0,
            "bytes_saved": 0,
        }

    def analyze(self, data: bytes) -> PatternResult:
        """
        Analyze a block of data for patterns.

        Args:
            data: Block data to analyze

        Returns:
            PatternResult with analysis details
        """
        if len(data) == 0:
            return PatternResult(
                is_zero_block=True,
                is_repeated_pattern=True,
                pattern_byte=0,
                unique_bytes_ratio=0.0,
            )

        self._stats["blocks_analyzed"] += 1

        # Check for zero block
        if all(b == 0 for b in data):
            self._stats["zero_blocks_found"] += 1
            return PatternResult(
                is_zero_block=True,
                is_repeated_pattern=True,
                pattern_byte=0,
                unique_bytes_ratio=0.0,
            )

        # Check for repeated pattern (all same byte)
        first_byte = data[0]
        if all(b == first_byte for b in data):
            self._stats["pattern_blocks_found"] += 1
            return PatternResult(
                is_zero_block=False,
                is_repeated_pattern=True,
                pattern_byte=first_byte,
                unique_bytes_ratio=0.0,
            )

        # Calculate unique bytes ratio for entropy estimation
        unique_bytes = len(set(data))
        unique_ratio = unique_bytes / 256.0  # Max 256 unique byte values

        return PatternResult(
            is_zero_block=False,
            is_repeated_pattern=False,
            pattern_byte=None,
            unique_bytes_ratio=unique_ratio,
        )

    def encode(self, data: bytes) -> tuple[bytes, bool]:
        """
        Encode data, replacing patterns with markers if applicable.

        Args:
            data: Original data block

        Returns:
            Tuple of (encoded data, was_reduced)
        """
        result = self.analyze(data)

        if result.is_zero_block:
            self._stats["bytes_saved"] += len(data) - len(self.ZERO_BLOCK_MARKER)
            return self.ZERO_BLOCK_MARKER, True

        if result.is_repeated_pattern and result.pattern_byte is not None:
            # Store pattern marker + the repeated byte + original length (4 bytes)
            encoded = (
                self.PATTERN_BLOCK_PREFIX
                + bytes([result.pattern_byte])
                + len(data).to_bytes(4, "big")
            )
            self._stats["bytes_saved"] += len(data) - len(encoded)
            return encoded, True

        return data, False

    def decode(self, data: bytes, original_size: int | None = None) -> bytes:
        """
        Decode data, expanding pattern markers back to full data.

        Args:
            data: Encoded data (may be marker or original)
            original_size: Expected original size (used for pattern expansion)

        Returns:
            Original data
        """
        if data == self.ZERO_BLOCK_MARKER:
            size = original_size or self.min_block_size
            return bytes(size)

        if data.startswith(self.PATTERN_BLOCK_PREFIX):
            pattern_byte = data[len(self.PATTERN_BLOCK_PREFIX)]
            size = int.from_bytes(data[-4:], "big")
            return bytes([pattern_byte] * size)

        return data

    def is_encoded(self, data: bytes) -> bool:
        """Check if data is a pattern-encoded marker."""
        return data == self.ZERO_BLOCK_MARKER or data.startswith(
            self.PATTERN_BLOCK_PREFIX
        )

    def find_zero_regions(
        self, data: bytes, min_region_size: int = 4096
    ) -> Iterator[tuple[int, int]]:
        """
        Find contiguous zero regions in data.

        Args:
            data: Data to scan
            min_region_size: Minimum size of zero region to report

        Yields:
            Tuples of (start_offset, length) for zero regions
        """
        start = None
        for i, byte in enumerate(data):
            if byte == 0:
                if start is None:
                    start = i
            else:
                if start is not None:
                    length = i - start
                    if length >= min_region_size:
                        yield (start, length)
                    start = None

        # Handle trailing zeros
        if start is not None:
            length = len(data) - start
            if length >= min_region_size:
                yield (start, length)

    def get_stats(self) -> dict:
        """Get pattern removal statistics."""
        return self._stats.copy()
