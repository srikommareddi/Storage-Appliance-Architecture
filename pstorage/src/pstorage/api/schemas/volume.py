"""Volume API schemas."""

from datetime import datetime

from pydantic import BaseModel, Field


class VolumeCreate(BaseModel):
    """Request to create a volume."""

    name: str = Field(..., min_length=1, max_length=255)
    size_gb: float = Field(..., gt=0, le=1024)
    description: str = ""
    tags: dict[str, str] = Field(default_factory=dict)


class VolumeUpdate(BaseModel):
    """Request to update a volume."""

    name: str | None = None
    description: str | None = None
    tags: dict[str, str] | None = None


class VolumeResize(BaseModel):
    """Request to resize a volume."""

    size_gb: float = Field(..., gt=0, le=1024)


class VolumeClone(BaseModel):
    """Request to clone a volume."""

    name: str = Field(..., min_length=1, max_length=255)
    description: str = ""


class VolumeResponse(BaseModel):
    """Volume response."""

    id: str
    name: str
    size_bytes: int
    used_bytes: int
    state: str
    description: str
    created_at: datetime
    updated_at: datetime
    tags: dict[str, str]
    reduction_ratio: float

    class Config:
        from_attributes = True


class VolumeListResponse(BaseModel):
    """List of volumes response."""

    volumes: list[VolumeResponse]
    total: int


class WriteRequest(BaseModel):
    """Request to write data to a volume."""

    offset: int = Field(..., ge=0)
    data: str = Field(..., description="Base64 encoded data")


class WriteResponse(BaseModel):
    """Write operation response."""

    volume_id: str
    offset: int
    size: int
    reduction_ratio: float
    was_deduplicated: bool
    was_compressed: bool


class ReadResponse(BaseModel):
    """Read operation response."""

    volume_id: str
    offset: int
    size: int
    data: str  # Base64 encoded
