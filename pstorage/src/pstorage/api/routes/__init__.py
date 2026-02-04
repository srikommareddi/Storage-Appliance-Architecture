"""API routes."""

from .analytics import router as analytics_router
from .metrics import router as metrics_router
from .replication import router as replication_router
from .snapshots import router as snapshots_router
from .volumes import router as volumes_router

__all__ = [
    "volumes_router",
    "snapshots_router",
    "replication_router",
    "metrics_router",
    "analytics_router",
]
