"""Storage Predictor - Predictive analytics for storage management."""

import asyncio
import time
from dataclasses import dataclass, field
from enum import Enum
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from ..core.controller import StorageController


class DataTemperature(Enum):
    """Data access temperature classification."""

    HOT = "hot"  # Accessed within last hour
    WARM = "warm"  # Accessed within last day
    COLD = "cold"  # Accessed within last week
    FROZEN = "frozen"  # Not accessed in over a week


class TierType(Enum):
    """Storage tier types."""

    NVME = "nvme"  # High-performance NVMe
    SSD = "ssd"  # Standard SSD
    HDD = "hdd"  # Spinning disk
    ARCHIVE = "archive"  # Cold storage/tape


@dataclass
class CapacityForecast:
    """Capacity forecast result."""

    current_used_bytes: int
    current_total_bytes: int
    current_utilization: float
    predicted_used_bytes_7d: int
    predicted_used_bytes_30d: int
    predicted_used_bytes_90d: int
    days_until_80_percent: int | None
    days_until_90_percent: int | None
    days_until_full: int | None
    growth_rate_bytes_per_day: float
    confidence: float  # 0.0 to 1.0


@dataclass
class DriveHealthScore:
    """Drive health assessment."""

    overall_score: float  # 0.0 to 100.0
    wear_level: float  # 0.0 to 100.0 (percentage of life used)
    erase_count_avg: float
    erase_count_max: int
    bad_block_count: int
    predicted_end_of_life_days: int | None
    status: str  # "healthy", "warning", "critical"
    recommendations: list[str] = field(default_factory=list)


@dataclass
class TieringRecommendation:
    """Data tiering recommendation."""

    volume_id: str
    volume_name: str
    current_tier: TierType
    recommended_tier: TierType
    temperature: DataTemperature
    last_access_seconds: float
    access_count: int
    estimated_savings_bytes: int
    reason: str


@dataclass
class AccessRecord:
    """Records access patterns for a volume/block."""

    volume_id: str
    block_offset: int
    access_count: int = 0
    last_access_time: float = 0.0
    total_bytes_accessed: int = 0


class StoragePredictor:
    """
    Predictive analytics engine for storage management.

    Provides:
    - Capacity forecasting using linear regression
    - Data temperature classification (hot/warm/cold/frozen)
    - Drive health scoring based on wear metrics
    - Auto-tiering recommendations
    """

    # Temperature thresholds (in seconds)
    TEMP_HOT_THRESHOLD = 3600  # 1 hour
    TEMP_WARM_THRESHOLD = 86400  # 1 day
    TEMP_COLD_THRESHOLD = 604800  # 1 week

    def __init__(self, controller: "StorageController"):
        self.controller = controller

        # Capacity history: list of (timestamp, used_bytes)
        self._capacity_history: list[tuple[float, int]] = []
        self._capacity_lock = asyncio.Lock()

        # Access tracking: volume_id -> block_offset -> AccessRecord
        self._access_records: dict[str, dict[int, AccessRecord]] = {}
        self._access_lock = asyncio.Lock()

        # Volume tier assignments
        self._volume_tiers: dict[str, TierType] = {}

        # Sampling configuration
        self._sample_interval = 300  # 5 minutes
        self._max_history_points = 2016  # ~1 week at 5-min intervals

    async def record_capacity_sample(self) -> None:
        """Record current capacity for forecasting."""
        stats = await self.controller.directflash.get_stats()
        used_bytes = stats["flash"]["used_bytes"]
        timestamp = time.time()

        async with self._capacity_lock:
            self._capacity_history.append((timestamp, used_bytes))

            # Trim old history
            if len(self._capacity_history) > self._max_history_points:
                self._capacity_history = self._capacity_history[-self._max_history_points :]

    async def record_access(
        self, volume_id: str, block_offset: int, size: int
    ) -> None:
        """Record block access for temperature tracking."""
        async with self._access_lock:
            if volume_id not in self._access_records:
                self._access_records[volume_id] = {}

            if block_offset not in self._access_records[volume_id]:
                self._access_records[volume_id][block_offset] = AccessRecord(
                    volume_id=volume_id,
                    block_offset=block_offset,
                )

            record = self._access_records[volume_id][block_offset]
            record.access_count += 1
            record.last_access_time = time.time()
            record.total_bytes_accessed += size

    async def get_capacity_forecast(self) -> CapacityForecast:
        """
        Generate capacity forecast using linear regression.

        Returns:
            CapacityForecast with predictions and growth rate
        """
        stats = await self.controller.directflash.get_stats()
        current_used = stats["flash"]["used_bytes"]
        total_capacity = stats["flash"]["total_capacity_bytes"]
        current_utilization = stats["flash"]["utilization"]

        async with self._capacity_lock:
            history = list(self._capacity_history)

        # Need at least 2 points for regression
        if len(history) < 2:
            # Not enough data - return current state with no predictions
            return CapacityForecast(
                current_used_bytes=current_used,
                current_total_bytes=total_capacity,
                current_utilization=current_utilization,
                predicted_used_bytes_7d=current_used,
                predicted_used_bytes_30d=current_used,
                predicted_used_bytes_90d=current_used,
                days_until_80_percent=None,
                days_until_90_percent=None,
                days_until_full=None,
                growth_rate_bytes_per_day=0.0,
                confidence=0.0,
            )

        # Linear regression: y = mx + b
        # where x = timestamp, y = used_bytes
        n = len(history)
        sum_x = sum(t for t, _ in history)
        sum_y = sum(u for _, u in history)
        sum_xy = sum(t * u for t, u in history)
        sum_x2 = sum(t * t for t, _ in history)

        # Slope (m) = (n*sum_xy - sum_x*sum_y) / (n*sum_x2 - sum_x^2)
        denominator = n * sum_x2 - sum_x * sum_x
        if denominator == 0:
            slope = 0.0
        else:
            slope = (n * sum_xy - sum_x * sum_y) / denominator

        # Intercept (b) = (sum_y - m*sum_x) / n
        intercept = (sum_y - slope * sum_x) / n

        # Convert slope to bytes per day (slope is bytes per second)
        growth_rate_per_day = slope * 86400

        # Calculate predictions
        now = time.time()

        def predict(days_ahead: int) -> int:
            future_time = now + (days_ahead * 86400)
            predicted = intercept + slope * future_time
            return max(current_used, min(int(predicted), total_capacity))

        # Calculate days until thresholds
        def days_until_threshold(threshold_ratio: float) -> int | None:
            if slope <= 0:
                return None
            target_bytes = int(total_capacity * threshold_ratio)
            if current_used >= target_bytes:
                return 0
            bytes_remaining = target_bytes - current_used
            days = bytes_remaining / max(growth_rate_per_day, 1)
            return max(0, int(days))

        # Calculate R-squared for confidence
        mean_y = sum_y / n
        ss_tot = sum((u - mean_y) ** 2 for _, u in history)
        ss_res = sum((u - (intercept + slope * t)) ** 2 for t, u in history)
        r_squared = 1 - (ss_res / ss_tot) if ss_tot > 0 else 0.0
        confidence = max(0.0, min(1.0, r_squared))

        return CapacityForecast(
            current_used_bytes=current_used,
            current_total_bytes=total_capacity,
            current_utilization=current_utilization,
            predicted_used_bytes_7d=predict(7),
            predicted_used_bytes_30d=predict(30),
            predicted_used_bytes_90d=predict(90),
            days_until_80_percent=days_until_threshold(0.80),
            days_until_90_percent=days_until_threshold(0.90),
            days_until_full=days_until_threshold(0.99),
            growth_rate_bytes_per_day=growth_rate_per_day,
            confidence=confidence,
        )

    async def get_data_temperature(
        self, volume_id: str, block_offset: int | None = None
    ) -> DataTemperature:
        """
        Classify data temperature based on access patterns.

        Args:
            volume_id: Volume ID to check
            block_offset: Optional specific block offset

        Returns:
            DataTemperature classification
        """
        now = time.time()

        async with self._access_lock:
            if volume_id not in self._access_records:
                return DataTemperature.FROZEN

            records = self._access_records[volume_id]

            if block_offset is not None:
                # Check specific block
                if block_offset not in records:
                    return DataTemperature.FROZEN
                last_access = records[block_offset].last_access_time
            else:
                # Check most recent access across all blocks
                if not records:
                    return DataTemperature.FROZEN
                last_access = max(r.last_access_time for r in records.values())

        age = now - last_access

        if age <= self.TEMP_HOT_THRESHOLD:
            return DataTemperature.HOT
        elif age <= self.TEMP_WARM_THRESHOLD:
            return DataTemperature.WARM
        elif age <= self.TEMP_COLD_THRESHOLD:
            return DataTemperature.COLD
        else:
            return DataTemperature.FROZEN

    async def get_drive_health(self) -> DriveHealthScore:
        """
        Assess drive health based on wear metrics.

        Returns:
            DriveHealthScore with health assessment and recommendations
        """
        stats = await self.controller.directflash.get_stats()
        block_stats = stats["blocks"]
        flash_stats = stats["flash"]

        avg_erase = block_stats["avg_erase_count"]
        max_erase = block_stats["max_erase_count"]
        bad_blocks = block_stats["bad_blocks"]
        total_blocks = block_stats["total_blocks"]

        # Assuming max endurance of 100,000 P/E cycles (configurable)
        max_endurance = 100000

        # Wear level as percentage of life used
        wear_level = (avg_erase / max_endurance) * 100

        # Calculate overall health score (100 = new, 0 = end of life)
        base_score = 100 - wear_level

        # Penalize for bad blocks
        bad_block_ratio = bad_blocks / total_blocks if total_blocks > 0 else 0
        bad_block_penalty = bad_block_ratio * 20  # Up to 20 point penalty

        # Penalize for wear variance (uneven wear)
        wear_variance = stats["wear_leveling"]["wear_variance"]
        variance_penalty = min(10, wear_variance / 1000)  # Up to 10 point penalty

        overall_score = max(0, base_score - bad_block_penalty - variance_penalty)

        # Predict end of life
        if avg_erase > 0 and max_erase < max_endurance:
            # Estimate based on current wear rate
            remaining_cycles = max_endurance - max_erase
            # Rough estimate: assume similar write pattern continues
            gc_stats = stats["garbage_collection"]
            if gc_stats["gc_runs"] > 0:
                cycles_per_day = (
                    gc_stats["blocks_reclaimed"] / max(1, gc_stats["gc_runs"])
                )
                if cycles_per_day > 0:
                    eol_days = int(remaining_cycles / cycles_per_day)
                else:
                    eol_days = None
            else:
                eol_days = None
        else:
            eol_days = None

        # Determine status and recommendations
        recommendations = []

        if overall_score >= 80:
            status = "healthy"
        elif overall_score >= 50:
            status = "warning"
            recommendations.append("Consider planning for drive replacement within 6 months")
            if wear_variance > 1000:
                recommendations.append("High wear variance detected - review wear leveling")
        else:
            status = "critical"
            recommendations.append("URGENT: Plan immediate drive replacement")
            recommendations.append("Consider migrating data to healthy drives")

        if bad_blocks > 0:
            recommendations.append(f"{bad_blocks} bad blocks detected - monitor closely")

        if flash_stats["utilization"] > 0.9:
            recommendations.append("Storage utilization above 90% - consider expansion")

        return DriveHealthScore(
            overall_score=overall_score,
            wear_level=wear_level,
            erase_count_avg=avg_erase,
            erase_count_max=max_erase,
            bad_block_count=bad_blocks,
            predicted_end_of_life_days=eol_days,
            status=status,
            recommendations=recommendations,
        )

    async def get_tiering_recommendations(self) -> list[TieringRecommendation]:
        """
        Generate data tiering recommendations.

        Returns:
            List of TieringRecommendation for volumes that should be moved
        """
        recommendations = []
        volumes = await self.controller.list_volumes()
        now = time.time()

        for volume in volumes:
            # Get volume temperature
            temperature = await self.get_data_temperature(volume.id)

            # Get current tier (default to NVMe)
            current_tier = self._volume_tiers.get(volume.id, TierType.NVME)

            # Determine recommended tier based on temperature
            if temperature == DataTemperature.HOT:
                recommended_tier = TierType.NVME
            elif temperature == DataTemperature.WARM:
                recommended_tier = TierType.SSD
            elif temperature == DataTemperature.COLD:
                recommended_tier = TierType.HDD
            else:  # FROZEN
                recommended_tier = TierType.ARCHIVE

            # Calculate last access time
            async with self._access_lock:
                if volume.id in self._access_records:
                    records = self._access_records[volume.id]
                    if records:
                        last_access = max(r.last_access_time for r in records.values())
                        access_count = sum(r.access_count for r in records.values())
                    else:
                        last_access = 0
                        access_count = 0
                else:
                    last_access = 0
                    access_count = 0

            last_access_seconds = now - last_access if last_access > 0 else float("inf")

            # Only recommend if tier should change
            if recommended_tier != current_tier:
                # Estimate savings (simplified)
                tier_cost_factors = {
                    TierType.NVME: 1.0,
                    TierType.SSD: 0.5,
                    TierType.HDD: 0.2,
                    TierType.ARCHIVE: 0.05,
                }
                current_cost = tier_cost_factors[current_tier]
                recommended_cost = tier_cost_factors[recommended_tier]

                if recommended_cost < current_cost:
                    savings_ratio = 1 - (recommended_cost / current_cost)
                    estimated_savings = int(volume.used_bytes * savings_ratio)
                else:
                    estimated_savings = 0  # Moving to faster tier costs more

                # Generate reason
                if recommended_tier.value < current_tier.value:
                    reason = f"Data is {temperature.value} - move to faster tier for better performance"
                else:
                    reason = f"Data is {temperature.value} - move to cheaper tier to reduce costs"

                recommendations.append(
                    TieringRecommendation(
                        volume_id=volume.id,
                        volume_name=volume.name,
                        current_tier=current_tier,
                        recommended_tier=recommended_tier,
                        temperature=temperature,
                        last_access_seconds=last_access_seconds,
                        access_count=access_count,
                        estimated_savings_bytes=estimated_savings,
                        reason=reason,
                    )
                )

        return recommendations

    async def set_volume_tier(self, volume_id: str, tier: TierType) -> None:
        """Set the tier assignment for a volume."""
        self._volume_tiers[volume_id] = tier

    async def get_volume_analytics(self, volume_id: str) -> dict:
        """Get comprehensive analytics for a specific volume."""
        volume = await self.controller.get_volume(volume_id)
        if not volume:
            return {"error": "Volume not found"}

        temperature = await self.get_data_temperature(volume_id)
        current_tier = self._volume_tiers.get(volume_id, TierType.NVME)

        async with self._access_lock:
            if volume_id in self._access_records:
                records = self._access_records[volume_id]
                total_accesses = sum(r.access_count for r in records.values())
                total_bytes_accessed = sum(
                    r.total_bytes_accessed for r in records.values()
                )
                hot_blocks = sum(
                    1
                    for r in records.values()
                    if time.time() - r.last_access_time <= self.TEMP_HOT_THRESHOLD
                )
            else:
                total_accesses = 0
                total_bytes_accessed = 0
                hot_blocks = 0

        return {
            "volume_id": volume_id,
            "volume_name": volume.name,
            "size_bytes": volume.size_bytes,
            "used_bytes": volume.used_bytes,
            "temperature": temperature.value,
            "current_tier": current_tier.value,
            "access_stats": {
                "total_accesses": total_accesses,
                "total_bytes_accessed": total_bytes_accessed,
                "hot_blocks": hot_blocks,
            },
        }

    async def get_stats(self) -> dict:
        """Get predictor statistics."""
        async with self._capacity_lock:
            capacity_samples = len(self._capacity_history)

        async with self._access_lock:
            tracked_volumes = len(self._access_records)
            tracked_blocks = sum(
                len(blocks) for blocks in self._access_records.values()
            )

        return {
            "capacity_samples": capacity_samples,
            "tracked_volumes": tracked_volumes,
            "tracked_blocks": tracked_blocks,
            "sample_interval_seconds": self._sample_interval,
            "max_history_points": self._max_history_points,
        }
