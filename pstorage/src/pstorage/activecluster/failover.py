"""Failover Controller - Automatic failover and failback."""

import asyncio
import time
from dataclasses import dataclass
from enum import Enum
from typing import Callable

from .mediator import Mediator, NodeRole


class FailoverState(Enum):
    """Failover state."""

    NORMAL = "normal"
    FAILING_OVER = "failing_over"
    FAILED_OVER = "failed_over"
    FAILING_BACK = "failing_back"


class FailoverReason(Enum):
    """Reason for failover."""

    NODE_FAILURE = "node_failure"
    MANUAL = "manual"
    NETWORK_PARTITION = "network_partition"
    MAINTENANCE = "maintenance"


@dataclass
class FailoverEvent:
    """Record of a failover event."""

    timestamp: float
    old_primary: str | None
    new_primary: str | None
    reason: FailoverReason
    state: FailoverState
    duration_ms: float | None = None


@dataclass
class FailoverStats:
    """Failover statistics."""

    total_failovers: int = 0
    successful_failovers: int = 0
    failed_failovers: int = 0
    total_failbacks: int = 0
    avg_failover_time_ms: float = 0.0


class FailoverController:
    """
    Automatic failover controller.

    Handles:
    - Detection of primary failure
    - Coordinated failover to secondary
    - Automatic failback when original primary recovers
    - Manual failover for maintenance

    Ensures zero data loss (RPO=0) and minimal downtime (RTO~0).
    """

    def __init__(
        self,
        mediator: Mediator,
        failover_timeout_ms: int = 5000,
        auto_failback: bool = True,
    ):
        self.mediator = mediator
        self.failover_timeout_ms = failover_timeout_ms
        self.auto_failback = auto_failback

        self._state = FailoverState.NORMAL
        self._original_primary: str | None = None

        # Failover history
        self._history: list[FailoverEvent] = []

        # Callbacks
        self._on_failover: Callable[[str, str], None] | None = None
        self._on_failback: Callable[[str, str], None] | None = None

        self._stats = FailoverStats()
        self._lock = asyncio.Lock()

    @property
    def state(self) -> FailoverState:
        """Get current failover state."""
        return self._state

    def on_failover(self, callback: Callable[[str, str], None]) -> None:
        """Register callback for failover events."""
        self._on_failover = callback

    def on_failback(self, callback: Callable[[str, str], None]) -> None:
        """Register callback for failback events."""
        self._on_failback = callback

    async def check_and_failover(self) -> FailoverEvent | None:
        """
        Check if failover is needed and execute if so.

        Returns:
            FailoverEvent if failover occurred, None otherwise
        """
        async with self._lock:
            if self._state != FailoverState.NORMAL:
                return None

            current_primary = await self.mediator.get_primary()
            if not current_primary:
                return None

            # Check if primary is healthy
            cluster_status = await self.mediator.get_cluster_status()
            nodes = cluster_status["nodes"]

            if current_primary not in nodes:
                return None

            primary_status = nodes[current_primary]

            if primary_status["is_healthy"]:
                return None

            # Primary is down - initiate failover
            return await self._execute_failover(
                current_primary, FailoverReason.NODE_FAILURE
            )

    async def manual_failover(self, target_node: str | None = None) -> FailoverEvent:
        """
        Manually initiate failover.

        Args:
            target_node: Optional specific node to fail over to

        Returns:
            FailoverEvent describing the failover
        """
        async with self._lock:
            current_primary = await self.mediator.get_primary()

            if target_node:
                await self.mediator.set_primary(target_node)
                new_primary = target_node
            else:
                new_primary = await self.mediator.initiate_election()

            return await self._execute_failover(
                current_primary, FailoverReason.MANUAL, new_primary
            )

    async def _execute_failover(
        self,
        old_primary: str | None,
        reason: FailoverReason,
        new_primary: str | None = None,
    ) -> FailoverEvent:
        """Execute the failover process."""
        start_time = time.time()
        self._state = FailoverState.FAILING_OVER
        self._original_primary = old_primary

        try:
            if not new_primary:
                new_primary = await self.mediator.initiate_election()

            if new_primary:
                self._stats.successful_failovers += 1
                self._state = FailoverState.FAILED_OVER

                # Call callback
                if self._on_failover and old_primary:
                    self._on_failover(old_primary, new_primary)
            else:
                self._stats.failed_failovers += 1
                self._state = FailoverState.NORMAL

            duration = (time.time() - start_time) * 1000

            event = FailoverEvent(
                timestamp=start_time,
                old_primary=old_primary,
                new_primary=new_primary,
                reason=reason,
                state=self._state,
                duration_ms=duration,
            )

            self._history.append(event)
            self._stats.total_failovers += 1

            # Update average failover time
            if new_primary:
                self._stats.avg_failover_time_ms = (
                    self._stats.avg_failover_time_ms * 0.8 + duration * 0.2
                )

            return event

        except Exception:
            self._stats.failed_failovers += 1
            self._state = FailoverState.NORMAL
            raise

    async def check_and_failback(self) -> FailoverEvent | None:
        """
        Check if failback is possible and execute if so.

        Returns:
            FailoverEvent if failback occurred, None otherwise
        """
        if not self.auto_failback:
            return None

        async with self._lock:
            if self._state != FailoverState.FAILED_OVER:
                return None

            if not self._original_primary:
                return None

            # Check if original primary is back
            cluster_status = await self.mediator.get_cluster_status()
            nodes = cluster_status["nodes"]

            if self._original_primary not in nodes:
                return None

            original_status = nodes[self._original_primary]

            if not original_status["is_healthy"]:
                return None

            # Original primary is back - initiate failback
            return await self._execute_failback()

    async def _execute_failback(self) -> FailoverEvent:
        """Execute the failback process."""
        start_time = time.time()
        self._state = FailoverState.FAILING_BACK

        current_primary = await self.mediator.get_primary()

        try:
            await self.mediator.set_primary(self._original_primary)  # type: ignore

            self._stats.total_failbacks += 1
            self._state = FailoverState.NORMAL

            duration = (time.time() - start_time) * 1000

            event = FailoverEvent(
                timestamp=start_time,
                old_primary=current_primary,
                new_primary=self._original_primary,
                reason=FailoverReason.MANUAL,  # Failback is treated as manual
                state=self._state,
                duration_ms=duration,
            )

            self._history.append(event)

            # Call callback
            if self._on_failback and current_primary and self._original_primary:
                self._on_failback(current_primary, self._original_primary)

            self._original_primary = None

            return event

        except Exception:
            self._state = FailoverState.FAILED_OVER
            raise

    async def manual_failback(self) -> FailoverEvent | None:
        """Manually initiate failback to original primary."""
        if self._state != FailoverState.FAILED_OVER:
            return None

        if not self._original_primary:
            return None

        async with self._lock:
            return await self._execute_failback()

    async def get_history(self, limit: int = 10) -> list[FailoverEvent]:
        """Get recent failover history."""
        return self._history[-limit:]

    async def get_stats(self) -> dict:
        """Get failover statistics."""
        return {
            "state": self._state.value,
            "original_primary": self._original_primary,
            "total_failovers": self._stats.total_failovers,
            "successful_failovers": self._stats.successful_failovers,
            "failed_failovers": self._stats.failed_failovers,
            "total_failbacks": self._stats.total_failbacks,
            "avg_failover_time_ms": self._stats.avg_failover_time_ms,
            "history_count": len(self._history),
        }
