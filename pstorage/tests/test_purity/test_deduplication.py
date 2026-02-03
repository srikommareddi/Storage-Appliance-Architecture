"""Tests for deduplication engine."""

import pytest

from pstorage.purity.deduplication import DeduplicationEngine


@pytest.fixture
def dedupe_engine():
    """Create deduplication engine for testing."""
    return DeduplicationEngine(min_block_size=4096, max_block_size=32768)


class TestDeduplication:
    """Deduplication engine tests."""

    @pytest.mark.asyncio
    async def test_fingerprint_computation(self, dedupe_engine):
        """Test fingerprint is computed correctly."""
        data = b"Hello, World!"
        fingerprint = dedupe_engine.compute_fingerprint(data)

        # SHA-256 should produce 64-character hex string
        assert len(fingerprint) == 64
        assert all(c in "0123456789abcdef" for c in fingerprint)

    @pytest.mark.asyncio
    async def test_same_data_same_fingerprint(self, dedupe_engine):
        """Test same data produces same fingerprint."""
        data = b"Test data block"
        fp1 = dedupe_engine.compute_fingerprint(data)
        fp2 = dedupe_engine.compute_fingerprint(data)

        assert fp1 == fp2

    @pytest.mark.asyncio
    async def test_different_data_different_fingerprint(self, dedupe_engine):
        """Test different data produces different fingerprint."""
        data1 = b"Data block 1"
        data2 = b"Data block 2"

        fp1 = dedupe_engine.compute_fingerprint(data1)
        fp2 = dedupe_engine.compute_fingerprint(data2)

        assert fp1 != fp2

    @pytest.mark.asyncio
    async def test_register_new_block(self, dedupe_engine):
        """Test registering a new unique block."""
        data = b"Unique data block"
        block_id = "block-001"

        fingerprint, is_new = await dedupe_engine.register_block(data, block_id)

        assert is_new is True
        assert len(fingerprint) == 64

        stats = await dedupe_engine.get_stats()
        assert stats["unique_blocks"] == 1
        assert stats["duplicate_blocks"] == 0

    @pytest.mark.asyncio
    async def test_detect_duplicate(self, dedupe_engine):
        """Test detecting duplicate data."""
        data = b"Duplicate data block"
        block_id1 = "block-001"
        block_id2 = "block-002"

        # Register first block
        fp1, is_new1 = await dedupe_engine.register_block(data, block_id1)
        assert is_new1 is True

        # Register same data again
        fp2, is_new2 = await dedupe_engine.register_block(data, block_id2)
        assert is_new2 is False
        assert fp1 == fp2

        stats = await dedupe_engine.get_stats()
        assert stats["unique_blocks"] == 1
        assert stats["duplicate_blocks"] == 1

    @pytest.mark.asyncio
    async def test_check_duplicate(self, dedupe_engine):
        """Test checking for duplicates."""
        data = b"Data to check"
        block_id = "block-001"

        # Check before registering
        result = await dedupe_engine.check_duplicate(data)
        assert result.is_duplicate is False

        # Register the block
        await dedupe_engine.register_block(data, block_id)

        # Check again
        result = await dedupe_engine.check_duplicate(data)
        assert result.is_duplicate is True
        assert result.existing_block_id == block_id

    @pytest.mark.asyncio
    async def test_dereference_block(self, dedupe_engine):
        """Test dereferencing a block."""
        data = b"Data to dereference"
        block_id = "block-001"

        # Register twice (simulating two references)
        await dedupe_engine.register_block(data, block_id)
        fp, _ = await dedupe_engine.register_block(data, block_id)

        # Dereference once
        ref_count = await dedupe_engine.dereference_block(fp)
        assert ref_count == 1

        # Dereference again
        ref_count = await dedupe_engine.dereference_block(fp)
        assert ref_count == 0

    @pytest.mark.asyncio
    async def test_dedupe_ratio(self, dedupe_engine):
        """Test deduplication ratio calculation."""
        data = b"A" * 4096  # 4KB block

        # Write same block 4 times
        for i in range(4):
            await dedupe_engine.register_block(data, f"block-{i}")

        stats = await dedupe_engine.get_stats()

        # 4x data in, 1x stored = 4:1 ratio
        assert stats["dedupe_ratio"] == 4.0
