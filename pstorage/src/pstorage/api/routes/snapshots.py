"""Snapshot API routes."""

from fastapi import APIRouter, HTTPException, status

from ..schemas.snapshot import (
    RestoreRequest,
    SnapshotCreate,
    SnapshotListResponse,
    SnapshotResponse,
)
from ..schemas.volume import VolumeResponse
from ...core.controller import get_controller

router = APIRouter(prefix="/volumes/{volume_id}/snapshots", tags=["snapshots"])


def snapshot_to_response(snapshot) -> SnapshotResponse:
    """Convert Snapshot to SnapshotResponse."""
    return SnapshotResponse(
        id=snapshot.id,
        name=snapshot.name,
        volume_id=snapshot.volume_id,
        volume_name=snapshot.volume_name,
        size_bytes=snapshot.size_bytes,
        used_bytes=snapshot.used_bytes,
        state=snapshot.state.value,
        description=snapshot.description,
        created_at=snapshot.created_at,
        tags=snapshot.tags,
    )


@router.post("", response_model=SnapshotResponse, status_code=status.HTTP_201_CREATED)
async def create_snapshot(volume_id: str, request: SnapshotCreate):
    """Create a snapshot of a volume."""
    controller = await get_controller()

    snapshot = await controller.create_snapshot(
        volume_id=volume_id,
        name=request.name,
        description=request.description,
    )

    if not snapshot:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Volume {volume_id} not found",
        )

    return snapshot_to_response(snapshot)


@router.get("", response_model=SnapshotListResponse)
async def list_snapshots(volume_id: str):
    """List all snapshots for a volume."""
    controller = await get_controller()

    # Verify volume exists
    volume = await controller.get_volume(volume_id)
    if not volume:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Volume {volume_id} not found",
        )

    snapshots = await controller.list_snapshots(volume_id)

    return SnapshotListResponse(
        snapshots=[snapshot_to_response(s) for s in snapshots],
        total=len(snapshots),
    )


@router.get("/{snapshot_id}", response_model=SnapshotResponse)
async def get_snapshot(volume_id: str, snapshot_id: str):
    """Get a specific snapshot."""
    controller = await get_controller()

    snapshot = await controller.get_snapshot(snapshot_id)

    if not snapshot or snapshot.volume_id != volume_id:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Snapshot {snapshot_id} not found",
        )

    return snapshot_to_response(snapshot)


@router.delete("/{snapshot_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_snapshot(volume_id: str, snapshot_id: str):
    """Delete a snapshot."""
    controller = await get_controller()

    snapshot = await controller.get_snapshot(snapshot_id)
    if not snapshot or snapshot.volume_id != volume_id:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Snapshot {snapshot_id} not found",
        )

    if not await controller.delete_snapshot(snapshot_id):
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Snapshot {snapshot_id} not found",
        )


@router.post("/{snapshot_id}/restore", response_model=VolumeResponse)
async def restore_snapshot(volume_id: str, snapshot_id: str, request: RestoreRequest):
    """Restore a volume from a snapshot."""
    controller = await get_controller()

    snapshot = await controller.get_snapshot(snapshot_id)
    if not snapshot or snapshot.volume_id != volume_id:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Snapshot {snapshot_id} not found",
        )

    volume = await controller.restore_snapshot(
        snapshot_id=snapshot_id,
        volume_id=request.target_volume_id,
    )

    if not volume:
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail="Failed to restore snapshot",
        )

    from .volumes import volume_to_response

    return volume_to_response(volume)


# Also add a global snapshots router for listing all snapshots
global_router = APIRouter(prefix="/snapshots", tags=["snapshots"])


@global_router.get("", response_model=SnapshotListResponse)
async def list_all_snapshots():
    """List all snapshots across all volumes."""
    controller = await get_controller()
    snapshots = await controller.list_snapshots()

    return SnapshotListResponse(
        snapshots=[snapshot_to_response(s) for s in snapshots],
        total=len(snapshots),
    )
