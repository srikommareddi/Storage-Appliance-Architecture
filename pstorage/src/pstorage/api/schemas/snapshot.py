"""Snapshot API schemas."""

from datetime import datetime

from pydantic import BaseModel, Field


class SnapshotCreate(BaseModel):
    """Request to create a snapshot."""

    name: str | None = Field(None, max_length=255)
    description: str = ""
    tags: dict[str, str] = Field(default_factory=dict)


class SnapshotResponse(BaseModel):
    """Snapshot response."""

    id: str
    name: str
    volume_id: str
    volume_name: str
    size_bytes: int
    used_bytes: int
    state: str
    description: str
    created_at: datetime
    tags: dict[str, str]

    class Config:
        from_attributes = True


class SnapshotListResponse(BaseModel):
    """List of snapshots response."""

    snapshots: list[SnapshotResponse]
    total: int


class RestoreRequest(BaseModel):
    """Request to restore from snapshot."""

    target_volume_id: str | None = None  # If None, creates new volume
