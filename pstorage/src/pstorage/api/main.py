"""FastAPI application entry point."""

import asyncio
from contextlib import asynccontextmanager

import uvicorn
from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from ..core.config import get_settings
from ..core.controller import get_controller
from ..activecluster.cluster import get_cluster
from .routes import metrics_router, replication_router, snapshots_router, volumes_router
from .routes.snapshots import global_router as global_snapshots_router


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Application lifespan handler."""
    # Startup
    controller = await get_controller()
    await controller.initialize()

    settings = get_settings()
    if settings.cluster_enabled:
        cluster = await get_cluster()
        if cluster:
            await cluster.initialize()

    yield

    # Shutdown
    await controller.shutdown()

    if settings.cluster_enabled:
        cluster = await get_cluster()
        if cluster:
            await cluster.shutdown()


app = FastAPI(
    title="PStorage API",
    description="""
    PStorage - Pure Storage-Inspired Storage Manager API

    A storage management system implementing:
    - **Data Reduction**: Deduplication, compression, pattern removal
    - **High Availability**: Active-active replication, automatic failover
    - **Flash Management**: Wear leveling, garbage collection
    - **RESTful APIs**: Volume, snapshot, and replication management
    """,
    version="0.1.0",
    lifespan=lifespan,
)

# CORS middleware
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# Include routers
app.include_router(volumes_router, prefix="/api/v1")
app.include_router(snapshots_router, prefix="/api/v1")
app.include_router(global_snapshots_router, prefix="/api/v1")
app.include_router(replication_router, prefix="/api/v1")
app.include_router(metrics_router, prefix="/api/v1")


@app.get("/")
async def root():
    """Root endpoint."""
    return {
        "name": "PStorage API",
        "version": "0.1.0",
        "description": "Pure Storage-Inspired Storage Manager",
    }


@app.get("/health")
async def health():
    """Health check endpoint."""
    controller = await get_controller()
    reduction_ratio = await controller.get_reduction_ratio()

    cluster = await get_cluster()
    cluster_healthy = True
    if cluster:
        health = await cluster.get_health()
        cluster_healthy = health.has_quorum

    return {
        "status": "healthy" if cluster_healthy else "degraded",
        "reduction_ratio": reduction_ratio,
        "cluster_enabled": cluster is not None,
    }


@app.get("/api/v1/arrays")
async def list_arrays():
    """List storage arrays (this node)."""
    settings = get_settings()
    controller = await get_controller()
    stats = await controller.get_stats()

    return {
        "arrays": [
            {
                "id": settings.node_id,
                "name": settings.app_name,
                "model": "PStorage Virtual Array",
                "version": "0.1.0",
                "capacity": {
                    "total_bytes": stats["flash"]["flash"]["total_capacity_bytes"],
                    "used_bytes": stats["flash"]["flash"]["used_bytes"],
                    "reduction_ratio": stats["data_reduction"]["overall"][
                        "total_reduction_ratio"
                    ],
                },
            }
        ]
    }


def run():
    """Run the API server."""
    settings = get_settings()
    uvicorn.run(
        "pstorage.api.main:app",
        host=settings.api_host,
        port=settings.api_port,
        reload=settings.debug,
    )


if __name__ == "__main__":
    run()
