"""Tests for compression engine."""

import pytest

from pstorage.purity.compression import CompressionAlgorithm, CompressionEngine


@pytest.fixture
def lz4_engine():
    """Create LZ4 compression engine."""
    return CompressionEngine(algorithm="lz4")


@pytest.fixture
def zstd_engine():
    """Create Zstd compression engine."""
    return CompressionEngine(algorithm="zstd", level=3)


class TestCompression:
    """Compression engine tests."""

    @pytest.mark.asyncio
    async def test_compress_decompress_lz4(self, lz4_engine):
        """Test LZ4 compression and decompression."""
        original = b"A" * 10000  # Highly compressible data

        compressed, result = lz4_engine.compress(original)
        decompressed = lz4_engine.decompress(compressed)

        assert decompressed == original
        assert result.was_compressed is True
        assert result.compression_ratio > 1.0

    @pytest.mark.asyncio
    async def test_compress_decompress_zstd(self, zstd_engine):
        """Test Zstd compression and decompression."""
        original = b"B" * 10000

        compressed, result = zstd_engine.compress(original)
        decompressed = zstd_engine.decompress(compressed)

        assert decompressed == original
        assert result.was_compressed is True

    @pytest.mark.asyncio
    async def test_incompressible_data(self, lz4_engine):
        """Test handling of incompressible data."""
        # Random-like data is hard to compress
        import os

        original = os.urandom(1000)

        compressed, result = lz4_engine.compress(original)
        decompressed = lz4_engine.decompress(compressed)

        assert decompressed == original
        # May or may not be compressed depending on entropy

    @pytest.mark.asyncio
    async def test_compression_ratio(self, lz4_engine):
        """Test compression ratio calculation."""
        # Very compressible data
        original = b"AAAA" * 1000  # 4000 bytes of repeated data

        _, result = lz4_engine.compress(original)

        assert result.original_size == 4000
        assert result.compressed_size < 4000
        assert result.compression_ratio > 1.0

    @pytest.mark.asyncio
    async def test_is_compressed(self, lz4_engine):
        """Test compressed data detection."""
        original = b"Test data" * 100

        compressed, _ = lz4_engine.compress(original)

        assert lz4_engine.is_compressed(compressed) is True
        assert lz4_engine.is_compressed(original) is False

    @pytest.mark.asyncio
    async def test_empty_data(self, lz4_engine):
        """Test handling of empty data."""
        original = b""

        compressed, result = lz4_engine.compress(original)
        decompressed = lz4_engine.decompress(compressed)

        assert decompressed == original

    @pytest.mark.asyncio
    async def test_stats(self, lz4_engine):
        """Test compression statistics."""
        data = b"X" * 5000

        lz4_engine.compress(data)
        lz4_engine.compress(data)

        stats = lz4_engine.get_stats()

        assert stats["bytes_before"] == 10000
        assert stats["compression_ratio"] > 1.0
