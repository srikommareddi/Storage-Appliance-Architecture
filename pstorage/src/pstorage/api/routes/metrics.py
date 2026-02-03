"""Metrics API routes."""

from fastapi import APIRouter

from ...activecluster.cluster import get_cluster
from ...core.controller import get_controller

router = APIRouter(prefix="/metrics", tags=["metrics"])


@router.get("")
async def get_metrics():
    """Get comprehensive system metrics."""
    controller = await get_controller()
    stats = await controller.get_stats()

    cluster = await get_cluster()
    if cluster:
        stats["cluster"] = await cluster.get_stats()

    return stats


@router.get("/reduction")
async def get_reduction_metrics():
    """Get data reduction metrics."""
    controller = await get_controller()
    purity_stats = await controller.purity.get_stats()

    return {
        "overall_reduction_ratio": purity_stats["overall"]["total_reduction_ratio"],
        "deduplication": {
            "ratio": purity_stats["deduplication"]["dedupe_ratio"],
            "unique_blocks": purity_stats["deduplication"]["unique_blocks"],
            "duplicate_blocks": purity_stats["deduplication"]["duplicate_blocks"],
        },
        "compression": {
            "ratio": purity_stats["compression"]["compression_ratio"],
            "algorithm": purity_stats["compression"]["algorithm"],
        },
        "pattern_removal": {
            "zero_blocks_found": purity_stats["pattern_removal"]["zero_blocks_found"],
            "pattern_blocks_found": purity_stats["pattern_removal"][
                "pattern_blocks_found"
            ],
        },
        "bytes_saved": {
            "pattern": purity_stats["overall"]["pattern_bytes_saved"],
            "dedupe": purity_stats["overall"]["dedupe_bytes_saved"],
            "compression": purity_stats["overall"]["compression_bytes_saved"],
        },
    }


@router.get("/flash")
async def get_flash_metrics():
    """Get flash/storage metrics."""
    controller = await get_controller()
    flash_stats = await controller.directflash.get_stats()

    return {
        "capacity": {
            "total_bytes": flash_stats["flash"]["total_capacity_bytes"],
            "used_bytes": flash_stats["flash"]["used_bytes"],
            "free_bytes": flash_stats["flash"]["free_bytes"],
            "utilization": flash_stats["flash"]["utilization"],
        },
        "health": {
            "health_percentage": flash_stats["flash"]["health_percentage"],
            "avg_erase_count": flash_stats["blocks"]["avg_erase_count"],
            "max_erase_count": flash_stats["blocks"]["max_erase_count"],
        },
        "wear_leveling": {
            "blocks_moved": flash_stats["wear_leveling"]["blocks_moved"],
            "wear_variance": flash_stats["wear_leveling"]["wear_variance"],
        },
        "garbage_collection": {
            "state": flash_stats["garbage_collection"]["state"],
            "blocks_reclaimed": flash_stats["garbage_collection"]["blocks_reclaimed"],
            "gc_runs": flash_stats["garbage_collection"]["gc_runs"],
        },
    }


@router.get("/volumes")
async def get_volume_metrics():
    """Get volume metrics."""
    controller = await get_controller()
    volume_stats = await controller.volume_manager.get_stats()
    snapshot_stats = await controller.snapshot_manager.get_stats()

    return {
        "volumes": {
            "total": volume_stats["total_volumes"],
            "provisioned_bytes": volume_stats["total_provisioned_bytes"],
            "used_bytes": volume_stats["total_used_bytes"],
        },
        "snapshots": {
            "total": snapshot_stats["total_snapshots"],
            "by_volume": snapshot_stats["snapshots_by_volume"],
        },
    }


@router.get("/replication")
async def get_replication_metrics():
    """Get replication metrics."""
    cluster = await get_cluster()

    if not cluster:
        return {
            "enabled": False,
            "message": "Clustering is not enabled",
        }

    replication_stats = await cluster.replication.get_stats()
    failover_stats = await cluster.failover.get_stats()

    return {
        "enabled": True,
        "mode": replication_stats["mode"],
        "peers": len(replication_stats["peers"]),
        "operations": {
            "replicated": replication_stats["operations_replicated"],
            "pending": replication_stats["operations_pending"],
            "errors": replication_stats["replication_errors"],
        },
        "latency_ms": replication_stats["avg_latency_ms"],
        "failover": {
            "state": failover_stats["state"],
            "total_failovers": failover_stats["total_failovers"],
            "avg_time_ms": failover_stats["avg_failover_time_ms"],
        },
    }
