"""File-based storage backend implementation."""

import asyncio
import hashlib
import json
from pathlib import Path
from typing import AsyncIterator

import aiofiles
import aiofiles.os

from ..core.config import Settings, get_settings
from .backend import BlockInfo, StorageBackend


class FileBackend(StorageBackend):
    """File-based storage backend for development and testing."""

    def __init__(self, settings: Settings | None = None):
        self.settings = settings or get_settings()
        self.data_dir = self.settings.data_dir
        self.metadata_dir = self.settings.metadata_dir
        self._lock = asyncio.Lock()
        self._initialized = False
        self._stats = {
            "blocks_written": 0,
            "blocks_read": 0,
            "blocks_deleted": 0,
            "bytes_written": 0,
            "bytes_read": 0,
        }

    async def initialize(self) -> None:
        """Initialize the file backend directories."""
        if self._initialized:
            return

        await aiofiles.os.makedirs(self.data_dir, exist_ok=True)
        await aiofiles.os.makedirs(self.metadata_dir, exist_ok=True)
        self._initialized = True

    async def shutdown(self) -> None:
        """Shutdown the file backend."""
        self._initialized = False

    def _block_path(self, block_id: str) -> Path:
        """Get the file path for a block."""
        # Use first 2 chars of block_id for subdirectory (sharding)
        subdir = block_id[:2] if len(block_id) >= 2 else "00"
        return self.data_dir / subdir / block_id

    def _metadata_path(self, block_id: str) -> Path:
        """Get the metadata file path for a block."""
        subdir = block_id[:2] if len(block_id) >= 2 else "00"
        return self.metadata_dir / subdir / f"{block_id}.json"

    async def write_block(self, block_id: str, data: bytes) -> BlockInfo:
        """Write a block to file storage."""
        async with self._lock:
            block_path = self._block_path(block_id)
            metadata_path = self._metadata_path(block_id)

            # Ensure directories exist
            await aiofiles.os.makedirs(block_path.parent, exist_ok=True)
            await aiofiles.os.makedirs(metadata_path.parent, exist_ok=True)

            # Calculate checksum
            checksum = hashlib.sha256(data).hexdigest()

            # Check if block already exists (dedup)
            existing_info = await self.get_block_info(block_id)
            if existing_info and existing_info.checksum == checksum:
                # Increment reference count
                existing_info.reference_count += 1
                await self._write_metadata(block_id, existing_info)
                return existing_info

            # Write data
            async with aiofiles.open(block_path, "wb") as f:
                await f.write(data)

            # Create block info
            info = BlockInfo(
                block_id=block_id,
                offset=0,
                size=len(data),
                checksum=checksum,
                compressed=False,
                deduplicated=False,
                reference_count=1,
            )

            # Write metadata
            await self._write_metadata(block_id, info)

            # Update stats
            self._stats["blocks_written"] += 1
            self._stats["bytes_written"] += len(data)

            return info

    async def _write_metadata(self, block_id: str, info: BlockInfo) -> None:
        """Write block metadata to file."""
        metadata_path = self._metadata_path(block_id)
        metadata = {
            "block_id": info.block_id,
            "offset": info.offset,
            "size": info.size,
            "checksum": info.checksum,
            "compressed": info.compressed,
            "deduplicated": info.deduplicated,
            "reference_count": info.reference_count,
        }
        async with aiofiles.open(metadata_path, "w") as f:
            await f.write(json.dumps(metadata))

    async def read_block(self, block_id: str) -> bytes:
        """Read a block from file storage."""
        block_path = self._block_path(block_id)

        if not block_path.exists():
            raise KeyError(f"Block not found: {block_id}")

        async with aiofiles.open(block_path, "rb") as f:
            data = await f.read()

        self._stats["blocks_read"] += 1
        self._stats["bytes_read"] += len(data)

        return data

    async def delete_block(self, block_id: str) -> bool:
        """Delete a block from file storage."""
        async with self._lock:
            info = await self.get_block_info(block_id)
            if not info:
                return False

            # Decrement reference count
            info.reference_count -= 1

            if info.reference_count <= 0:
                # Actually delete the block
                block_path = self._block_path(block_id)
                metadata_path = self._metadata_path(block_id)

                try:
                    await aiofiles.os.remove(block_path)
                    await aiofiles.os.remove(metadata_path)
                except FileNotFoundError:
                    return False

                self._stats["blocks_deleted"] += 1
            else:
                # Just update metadata
                await self._write_metadata(block_id, info)

            return True

    async def block_exists(self, block_id: str) -> bool:
        """Check if a block exists."""
        return self._block_path(block_id).exists()

    async def get_block_info(self, block_id: str) -> BlockInfo | None:
        """Get metadata for a block."""
        metadata_path = self._metadata_path(block_id)

        if not metadata_path.exists():
            return None

        try:
            async with aiofiles.open(metadata_path, "r") as f:
                content = await f.read()
                metadata = json.loads(content)
                return BlockInfo(**metadata)
        except (json.JSONDecodeError, FileNotFoundError):
            return None

    async def list_blocks(self) -> AsyncIterator[str]:
        """List all block IDs."""
        if not self.data_dir.exists():
            return

        for subdir in self.data_dir.iterdir():
            if subdir.is_dir():
                for block_file in subdir.iterdir():
                    if block_file.is_file():
                        yield block_file.name

    async def get_stats(self) -> dict:
        """Get storage statistics."""
        total_blocks = 0
        total_bytes = 0

        async for block_id in self.list_blocks():
            total_blocks += 1
            info = await self.get_block_info(block_id)
            if info:
                total_bytes += info.size

        return {
            **self._stats,
            "total_blocks": total_blocks,
            "total_bytes": total_bytes,
            "data_dir": str(self.data_dir),
        }
