"""Configuration management for PStorage."""

from functools import lru_cache
from pathlib import Path
from typing import Literal

from pydantic import Field
from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    """PStorage configuration settings."""

    # General settings
    app_name: str = "PStorage"
    debug: bool = False
    log_level: str = "INFO"

    # Storage settings
    data_dir: Path = Field(default=Path("/tmp/pstorage/data"))
    metadata_dir: Path = Field(default=Path("/tmp/pstorage/metadata"))
    max_volume_size_gb: int = 1024  # 1TB max volume size
    default_block_size: int = 4096  # 4KB blocks

    # DirectFlash settings
    flash_page_size: int = 4096  # 4KB pages
    flash_block_size: int = 262144  # 256KB blocks (64 pages)
    total_flash_blocks: int = 1024  # Simulated flash capacity
    gc_threshold: float = 0.75  # Start GC when 75% full
    gc_target: float = 0.50  # GC until 50% free
    wear_leveling_threshold: int = 1000  # P/E cycle threshold

    # Purity data reduction settings
    dedupe_enabled: bool = True
    compression_enabled: bool = True
    pattern_removal_enabled: bool = True
    dedupe_block_size_min: int = 4096  # 4KB min
    dedupe_block_size_max: int = 32768  # 32KB max
    compression_algorithm: Literal["lz4", "zstd"] = "lz4"
    compression_level: int = 1  # 1-9 for zstd, ignored for lz4

    # ActiveCluster settings
    cluster_enabled: bool = False
    node_id: str = "node-1"
    peer_nodes: list[str] = Field(default_factory=list)
    replication_mode: Literal["sync", "async"] = "sync"
    heartbeat_interval_ms: int = 1000
    failover_timeout_ms: int = 5000
    mediator_url: str | None = None

    # API settings
    api_host: str = "0.0.0.0"
    api_port: int = 8000

    model_config = {
        "env_prefix": "PSTORAGE_",
        "env_file": ".env",
    }


@lru_cache
def get_settings() -> Settings:
    """Get cached settings instance."""
    return Settings()
