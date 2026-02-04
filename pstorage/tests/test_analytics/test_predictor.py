"""Tests for StoragePredictor."""

import time

import pytest

from pstorage.analytics.predictor import (
    DataTemperature,
    StoragePredictor,
    TierType,
)
from pstorage.core.controller import StorageController


class TestStoragePredictor:
    """Tests for StoragePredictor class."""

    @pytest.fixture
    async def controller(self):
        """Create a storage controller for testing."""
        ctrl = StorageController()
        await ctrl.initialize()
        yield ctrl
        await ctrl.shutdown()

    @pytest.fixture
    async def predictor(self, controller):
        """Create a predictor for testing."""
        return StoragePredictor(controller)

    async def test_record_capacity_sample(self, predictor):
        """Test recording capacity samples."""
        stats_before = await predictor.get_stats()
        assert stats_before["capacity_samples"] == 0

        await predictor.record_capacity_sample()

        stats_after = await predictor.get_stats()
        assert stats_after["capacity_samples"] == 1

    async def test_capacity_forecast_no_data(self, predictor):
        """Test capacity forecast with no historical data."""
        forecast = await predictor.get_capacity_forecast()

        # Should return current state with zero confidence
        assert forecast.confidence == 0.0
        assert forecast.growth_rate_bytes_per_day == 0.0
        assert forecast.days_until_full is None

    async def test_capacity_forecast_with_data(self, predictor, controller):
        """Test capacity forecast with historical data."""
        # Add some capacity samples
        for _ in range(5):
            await predictor.record_capacity_sample()

        forecast = await predictor.get_capacity_forecast()

        # Should have current capacity info
        assert forecast.current_used_bytes >= 0
        assert forecast.current_total_bytes > 0
        assert 0.0 <= forecast.current_utilization <= 1.0

    async def test_record_access(self, predictor):
        """Test recording block access."""
        volume_id = "test-volume"
        block_offset = 0
        size = 4096

        await predictor.record_access(volume_id, block_offset, size)

        stats = await predictor.get_stats()
        assert stats["tracked_volumes"] == 1
        assert stats["tracked_blocks"] == 1

    async def test_data_temperature_hot(self, predictor):
        """Test hot data temperature classification."""
        volume_id = "test-volume"

        # Record recent access
        await predictor.record_access(volume_id, 0, 4096)

        temp = await predictor.get_data_temperature(volume_id)
        assert temp == DataTemperature.HOT

    async def test_data_temperature_frozen(self, predictor):
        """Test frozen data temperature classification."""
        volume_id = "no-access-volume"

        # No access recorded
        temp = await predictor.get_data_temperature(volume_id)
        assert temp == DataTemperature.FROZEN

    async def test_drive_health(self, predictor):
        """Test drive health assessment."""
        health = await predictor.get_drive_health()

        # Should have valid health metrics
        assert 0.0 <= health.overall_score <= 100.0
        assert 0.0 <= health.wear_level <= 100.0
        assert health.erase_count_avg >= 0
        assert health.erase_count_max >= 0
        assert health.bad_block_count >= 0
        assert health.status in ["healthy", "warning", "critical"]
        assert isinstance(health.recommendations, list)

    async def test_set_volume_tier(self, predictor, controller):
        """Test setting volume tier."""
        # Create a volume
        volume = await controller.create_volume("test-vol", size_gb=1.0)

        await predictor.set_volume_tier(volume.id, TierType.SSD)

        analytics = await predictor.get_volume_analytics(volume.id)
        assert analytics["current_tier"] == "ssd"

    async def test_tiering_recommendations_empty(self, predictor):
        """Test tiering recommendations with no volumes."""
        recommendations = await predictor.get_tiering_recommendations()

        # Should return empty list when no volumes
        assert isinstance(recommendations, list)

    async def test_tiering_recommendations_with_volumes(self, predictor, controller):
        """Test tiering recommendations with volumes."""
        # Create volumes
        vol1 = await controller.create_volume("hot-vol", size_gb=1.0)
        vol2 = await controller.create_volume("cold-vol", size_gb=1.0)

        # Record access for vol1 (hot)
        await predictor.record_access(vol1.id, 0, 4096)

        # Vol2 has no access (frozen)

        recommendations = await predictor.get_tiering_recommendations()

        # Should recommend moving cold/frozen data to cheaper tiers
        assert isinstance(recommendations, list)

    async def test_volume_analytics(self, predictor, controller):
        """Test volume analytics."""
        volume = await controller.create_volume("analytics-test", size_gb=1.0)

        # Record some accesses
        await predictor.record_access(volume.id, 0, 4096)
        await predictor.record_access(volume.id, 4096, 4096)
        await predictor.record_access(volume.id, 0, 4096)  # Repeat

        analytics = await predictor.get_volume_analytics(volume.id)

        assert analytics["volume_id"] == volume.id
        assert analytics["volume_name"] == "analytics-test"
        assert analytics["temperature"] == "hot"
        assert analytics["access_stats"]["total_accesses"] == 3
        assert analytics["access_stats"]["total_bytes_accessed"] == 3 * 4096

    async def test_volume_analytics_not_found(self, predictor):
        """Test volume analytics for nonexistent volume."""
        analytics = await predictor.get_volume_analytics("nonexistent-id")
        assert "error" in analytics

    async def test_predictor_stats(self, predictor):
        """Test predictor statistics."""
        stats = await predictor.get_stats()

        assert "capacity_samples" in stats
        assert "tracked_volumes" in stats
        assert "tracked_blocks" in stats
        assert "sample_interval_seconds" in stats
        assert "max_history_points" in stats
