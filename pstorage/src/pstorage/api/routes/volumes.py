"""Volume API routes."""

import base64

from fastapi import APIRouter, HTTPException, status

from ..schemas.volume import (
    ReadResponse,
    VolumeClone,
    VolumeCreate,
    VolumeListResponse,
    VolumeResize,
    VolumeResponse,
    VolumeUpdate,
    WriteRequest,
    WriteResponse,
)
from ...core.controller import get_controller

router = APIRouter(prefix="/volumes", tags=["volumes"])


def volume_to_response(volume) -> VolumeResponse:
    """Convert Volume to VolumeResponse."""
    return VolumeResponse(
        id=volume.id,
        name=volume.name,
        size_bytes=volume.size_bytes,
        used_bytes=volume.used_bytes,
        state=volume.state.value,
        description=volume.description,
        created_at=volume.created_at,
        updated_at=volume.updated_at,
        tags=volume.tags,
        reduction_ratio=volume.reduction_ratio,
    )


@router.post("", response_model=VolumeResponse, status_code=status.HTTP_201_CREATED)
async def create_volume(request: VolumeCreate):
    """Create a new storage volume."""
    controller = await get_controller()

    volume = await controller.create_volume(
        name=request.name,
        size_gb=request.size_gb,
        description=request.description,
        tags=request.tags,
    )

    return volume_to_response(volume)


@router.get("", response_model=VolumeListResponse)
async def list_volumes():
    """List all storage volumes."""
    controller = await get_controller()
    volumes = await controller.list_volumes()

    return VolumeListResponse(
        volumes=[volume_to_response(v) for v in volumes],
        total=len(volumes),
    )


@router.get("/{volume_id}", response_model=VolumeResponse)
async def get_volume(volume_id: str):
    """Get a specific volume by ID."""
    controller = await get_controller()
    volume = await controller.get_volume(volume_id)

    if not volume:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Volume {volume_id} not found",
        )

    return volume_to_response(volume)


@router.put("/{volume_id}", response_model=VolumeResponse)
async def update_volume(volume_id: str, request: VolumeUpdate):
    """Update volume metadata."""
    controller = await get_controller()

    volume = await controller.volume_manager.update_volume(
        volume_id=volume_id,
        name=request.name,
        description=request.description,
        tags=request.tags,
    )

    if not volume:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Volume {volume_id} not found",
        )

    return volume_to_response(volume)


@router.delete("/{volume_id}", status_code=status.HTTP_204_NO_CONTENT)
async def delete_volume(volume_id: str):
    """Delete a volume."""
    controller = await get_controller()

    if not await controller.delete_volume(volume_id):
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Volume {volume_id} not found",
        )


@router.post("/{volume_id}/resize", response_model=VolumeResponse)
async def resize_volume(volume_id: str, request: VolumeResize):
    """Resize a volume (expand only)."""
    controller = await get_controller()

    try:
        volume = await controller.volume_manager.resize_volume(
            volume_id=volume_id,
            new_size_bytes=int(request.size_gb * 1024 * 1024 * 1024),
        )
    except ValueError as e:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail=str(e),
        )

    if not volume:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Volume {volume_id} not found",
        )

    return volume_to_response(volume)


@router.post("/{volume_id}/clone", response_model=VolumeResponse)
async def clone_volume(volume_id: str, request: VolumeClone):
    """Clone a volume (copy-on-write)."""
    controller = await get_controller()

    volume = await controller.clone_volume(
        source_id=volume_id,
        new_name=request.name,
        description=request.description,
    )

    if not volume:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Volume {volume_id} not found",
        )

    return volume_to_response(volume)


@router.post("/{volume_id}/write", response_model=WriteResponse)
async def write_data(volume_id: str, request: WriteRequest):
    """Write data to a volume."""
    controller = await get_controller()

    # Decode base64 data
    try:
        data = base64.b64decode(request.data)
    except Exception:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail="Invalid base64 data",
        )

    try:
        result = await controller.write(
            volume_id=volume_id,
            offset=request.offset,
            data=data,
        )
    except ValueError as e:
        raise HTTPException(
            status_code=status.HTTP_400_BAD_REQUEST,
            detail=str(e),
        )

    if not result:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Volume {volume_id} not found",
        )

    return WriteResponse(
        volume_id=result.volume_id,
        offset=result.offset,
        size=result.size,
        reduction_ratio=result.reduction.reduction_ratio,
        was_deduplicated=result.reduction.was_deduplicated,
        was_compressed=result.reduction.was_compressed,
    )


@router.get("/{volume_id}/read", response_model=ReadResponse)
async def read_data(volume_id: str, offset: int = 0):
    """Read data from a volume."""
    controller = await get_controller()

    result = await controller.read(
        volume_id=volume_id,
        offset=offset,
    )

    if not result:
        raise HTTPException(
            status_code=status.HTTP_404_NOT_FOUND,
            detail=f"Data not found at offset {offset}",
        )

    return ReadResponse(
        volume_id=result.volume_id,
        offset=result.offset,
        size=result.size,
        data=base64.b64encode(result.data).decode(),
    )
