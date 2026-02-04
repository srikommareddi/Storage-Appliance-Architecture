"""Analytics API routes for predictive storage management."""

from fastapi import APIRouter, HTTPException

from ...analytics.predictor import StoragePredictor, TierType
from ...core.controller import get_controller

router = APIRouter(prefix="/analytics", tags=["analytics"])

# Singleton predictor instance
_predictor: StoragePredictor | None = None


async def get_predictor() -> StoragePredictor:
    """Get or create the storage predictor singleton."""
    global _predictor
    if _predictor is None:
        controller = await get_controller()
        _predictor = StoragePredictor(controller)
    return _predictor


@router.get("")
async def get_analytics_overview():
    """Get analytics system overview."""
    predictor = await get_predictor()
    stats = await predictor.get_stats()
    return {
        "status": "active",
        "predictor_stats": stats,
    }


@router.get("/capacity/forecast")
async def get_capacity_forecast():
    """
    Get capacity forecast using linear regression.

    Returns predictions for 7, 30, and 90 days, along with
    estimated days until capacity thresholds are reached.
    """
    predictor = await get_predictor()
    forecast = await predictor.get_capacity_forecast()

    return {
        "current": {
            "used_bytes": forecast.current_used_bytes,
            "total_bytes": forecast.current_total_bytes,
            "utilization_percent": round(forecast.current_utilization * 100, 2),
        },
        "predictions": {
            "7_days": {
                "used_bytes": forecast.predicted_used_bytes_7d,
                "utilization_percent": round(
                    forecast.predicted_used_bytes_7d / forecast.current_total_bytes * 100, 2
                ),
            },
            "30_days": {
                "used_bytes": forecast.predicted_used_bytes_30d,
                "utilization_percent": round(
                    forecast.predicted_used_bytes_30d / forecast.current_total_bytes * 100, 2
                ),
            },
            "90_days": {
                "used_bytes": forecast.predicted_used_bytes_90d,
                "utilization_percent": round(
                    forecast.predicted_used_bytes_90d / forecast.current_total_bytes * 100, 2
                ),
            },
        },
        "thresholds": {
            "days_until_80_percent": forecast.days_until_80_percent,
            "days_until_90_percent": forecast.days_until_90_percent,
            "days_until_full": forecast.days_until_full,
        },
        "growth_rate": {
            "bytes_per_day": forecast.growth_rate_bytes_per_day,
            "gb_per_day": round(forecast.growth_rate_bytes_per_day / (1024**3), 4),
        },
        "confidence": round(forecast.confidence, 3),
    }


@router.post("/capacity/sample")
async def record_capacity_sample():
    """Record a capacity sample for forecasting."""
    predictor = await get_predictor()
    await predictor.record_capacity_sample()
    return {"status": "recorded"}


@router.get("/health")
async def get_drive_health():
    """
    Get drive health assessment.

    Returns overall health score, wear metrics, and recommendations.
    """
    predictor = await get_predictor()
    health = await predictor.get_drive_health()

    return {
        "overall_score": round(health.overall_score, 2),
        "status": health.status,
        "wear_metrics": {
            "wear_level_percent": round(health.wear_level, 2),
            "erase_count_avg": round(health.erase_count_avg, 2),
            "erase_count_max": health.erase_count_max,
            "bad_block_count": health.bad_block_count,
        },
        "predictions": {
            "end_of_life_days": health.predicted_end_of_life_days,
        },
        "recommendations": health.recommendations,
    }


@router.get("/tiering/recommendations")
async def get_tiering_recommendations():
    """
    Get data tiering recommendations.

    Analyzes volume access patterns and recommends tier changes
    to optimize cost and performance.
    """
    predictor = await get_predictor()
    recommendations = await predictor.get_tiering_recommendations()

    return {
        "count": len(recommendations),
        "recommendations": [
            {
                "volume_id": r.volume_id,
                "volume_name": r.volume_name,
                "current_tier": r.current_tier.value,
                "recommended_tier": r.recommended_tier.value,
                "temperature": r.temperature.value,
                "last_access_seconds": r.last_access_seconds,
                "access_count": r.access_count,
                "estimated_savings_bytes": r.estimated_savings_bytes,
                "reason": r.reason,
            }
            for r in recommendations
        ],
    }


@router.get("/temperature/{volume_id}")
async def get_volume_temperature(volume_id: str):
    """Get data temperature classification for a volume."""
    predictor = await get_predictor()
    temperature = await predictor.get_data_temperature(volume_id)

    return {
        "volume_id": volume_id,
        "temperature": temperature.value,
        "thresholds": {
            "hot_seconds": predictor.TEMP_HOT_THRESHOLD,
            "warm_seconds": predictor.TEMP_WARM_THRESHOLD,
            "cold_seconds": predictor.TEMP_COLD_THRESHOLD,
        },
    }


@router.get("/volume/{volume_id}")
async def get_volume_analytics(volume_id: str):
    """Get comprehensive analytics for a specific volume."""
    predictor = await get_predictor()
    analytics = await predictor.get_volume_analytics(volume_id)

    if "error" in analytics:
        raise HTTPException(status_code=404, detail=analytics["error"])

    return analytics


@router.post("/volume/{volume_id}/tier")
async def set_volume_tier(volume_id: str, tier: str):
    """
    Set the tier assignment for a volume.

    Valid tiers: nvme, ssd, hdd, archive
    """
    try:
        tier_type = TierType(tier.lower())
    except ValueError:
        raise HTTPException(
            status_code=400,
            detail=f"Invalid tier. Valid options: {[t.value for t in TierType]}",
        )

    predictor = await get_predictor()

    # Verify volume exists
    controller = await get_controller()
    volume = await controller.get_volume(volume_id)
    if not volume:
        raise HTTPException(status_code=404, detail="Volume not found")

    await predictor.set_volume_tier(volume_id, tier_type)

    return {
        "volume_id": volume_id,
        "tier": tier_type.value,
        "status": "updated",
    }


@router.post("/access/record")
async def record_access(volume_id: str, block_offset: int, size: int):
    """Record a block access for temperature tracking."""
    predictor = await get_predictor()
    await predictor.record_access(volume_id, block_offset, size)
    return {"status": "recorded"}
