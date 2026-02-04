#!/usr/bin/env python3
"""
PStorage Demo Script

Showcases key capabilities:
1. Volume management
2. Data reduction (dedupe + compression)
3. Predictive analytics (capacity, health, tiering)
4. Snapshot operations

Usage:
    # Start the API server first
    uvicorn pstorage.api.main:app --reload

    # Run demo
    python demo.py
"""

import asyncio
import json
import sys
import time

import httpx

BASE_URL = "http://localhost:8000"
API_V1 = f"{BASE_URL}/api/v1"


def print_header(title: str) -> None:
    """Print a section header."""
    print(f"\n{'='*60}")
    print(f"  {title}")
    print(f"{'='*60}\n")


def print_json(data: dict) -> None:
    """Pretty print JSON data."""
    print(json.dumps(data, indent=2, default=str))


async def check_server() -> bool:
    """Check if server is running."""
    try:
        async with httpx.AsyncClient() as client:
            response = await client.get(f"{BASE_URL}/health", timeout=5.0)
            return response.status_code == 200
    except Exception:
        return False


async def demo_volumes(client: httpx.AsyncClient) -> str:
    """Demo: Volume Management"""
    print_header("1. VOLUME MANAGEMENT")

    # Create volume
    print("Creating volume 'demo-volume' (10 GB)...")
    response = await client.post(
        f"{API_V1}/volumes",
        json={"name": "demo-volume", "size_gb": 10, "description": "Demo volume"},
    )
    volume = response.json()
    volume_id = volume["id"]
    print(f"  Created: {volume_id[:8]}...")
    print(f"  Size: {volume['size_bytes'] / (1024**3):.1f} GB")

    # List volumes
    print("\nListing all volumes...")
    response = await client.get(f"{API_V1}/volumes")
    volumes = response.json()
    print(f"  Total volumes: {len(volumes)}")

    return volume_id


async def demo_data_reduction(client: httpx.AsyncClient, volume_id: str) -> None:
    """Demo: Data Reduction"""
    print_header("2. DATA REDUCTION (Dedupe + Compression)")

    import base64

    # Write duplicate data blocks
    print("Writing 5 identical 4KB blocks (should dedupe to 1)...")
    data = b"A" * 4096  # Highly compressible
    encoded = base64.b64encode(data).decode()

    for i in range(5):
        offset = i * 4096
        await client.post(
            f"{API_V1}/volumes/{volume_id}/write",
            json={"offset": offset, "data": encoded},
        )
        print(f"  Block {i+1}: offset={offset}")

    # Check reduction metrics
    print("\nChecking data reduction metrics...")
    response = await client.get(f"{API_V1}/metrics/reduction")
    metrics = response.json()

    print(f"  Deduplication ratio: {metrics['deduplication']['ratio']:.2f}x")
    print(f"  Unique blocks: {metrics['deduplication']['unique_blocks']}")
    print(f"  Duplicate blocks: {metrics['deduplication']['duplicate_blocks']}")
    print(f"  Compression ratio: {metrics['compression']['ratio']:.2f}x")
    print(f"  Algorithm: {metrics['compression']['algorithm']}")


async def demo_predictive_analytics(client: httpx.AsyncClient) -> None:
    """Demo: Predictive Analytics"""
    print_header("3. PREDICTIVE ANALYTICS")

    # Capacity Forecast
    print("3a. Capacity Forecasting")
    print("-" * 40)

    # Record some capacity samples first
    for _ in range(3):
        await client.post(f"{API_V1}/analytics/capacity/sample")
        await asyncio.sleep(0.1)

    response = await client.get(f"{API_V1}/analytics/capacity/forecast")
    forecast = response.json()

    print(f"  Current utilization: {forecast['current']['utilization_percent']:.1f}%")
    print(f"  Predicted (7 days):  {forecast['predictions']['7_days']['utilization_percent']:.1f}%")
    print(f"  Predicted (30 days): {forecast['predictions']['30_days']['utilization_percent']:.1f}%")
    print(f"  Confidence: {forecast['confidence']:.1%}")

    if forecast["thresholds"]["days_until_80_percent"]:
        print(f"  Days until 80% full: {forecast['thresholds']['days_until_80_percent']}")

    # Drive Health
    print("\n3b. Drive Health Assessment")
    print("-" * 40)

    response = await client.get(f"{API_V1}/analytics/health")
    health = response.json()

    print(f"  Overall score: {health['overall_score']:.1f}/100")
    print(f"  Status: {health['status'].upper()}")
    print(f"  Wear level: {health['wear_metrics']['wear_level_percent']:.2f}%")
    print(f"  Bad blocks: {health['wear_metrics']['bad_block_count']}")

    if health["recommendations"]:
        print("  Recommendations:")
        for rec in health["recommendations"]:
            print(f"    - {rec}")

    # Tiering Recommendations
    print("\n3c. Data Tiering Recommendations")
    print("-" * 40)

    response = await client.get(f"{API_V1}/analytics/tiering/recommendations")
    tiering = response.json()

    if tiering["count"] == 0:
        print("  No tier changes recommended (all data optimally placed)")
    else:
        print(f"  {tiering['count']} recommendation(s):")
        for rec in tiering["recommendations"]:
            print(f"    - {rec['volume_name']}: {rec['current_tier']} -> {rec['recommended_tier']}")
            print(f"      Reason: {rec['reason']}")


async def demo_snapshots(client: httpx.AsyncClient, volume_id: str) -> None:
    """Demo: Snapshot Operations"""
    print_header("4. SNAPSHOT OPERATIONS")

    # Create snapshot
    print("Creating snapshot 'demo-snap-1'...")
    response = await client.post(
        f"{API_V1}/volumes/{volume_id}/snapshots",
        json={"name": "demo-snap-1", "description": "Demo snapshot"},
    )
    snapshot = response.json()
    print(f"  Snapshot ID: {snapshot['id'][:8]}...")
    print(f"  Created at: {snapshot['created_at']}")

    # List snapshots
    print("\nListing volume snapshots...")
    response = await client.get(f"{API_V1}/volumes/{volume_id}/snapshots")
    snapshots = response.json()
    print(f"  Total snapshots: {len(snapshots)}")


async def demo_flash_metrics(client: httpx.AsyncClient) -> None:
    """Demo: Flash Management Metrics"""
    print_header("5. FLASH MANAGEMENT")

    response = await client.get(f"{API_V1}/metrics/flash")
    flash = response.json()

    print("Capacity:")
    print(f"  Total: {flash['capacity']['total_bytes'] / (1024**3):.1f} GB")
    print(f"  Used: {flash['capacity']['used_bytes'] / (1024**3):.3f} GB")
    print(f"  Utilization: {flash['capacity']['utilization'] * 100:.1f}%")

    print("\nHealth:")
    print(f"  Health: {flash['health']['health_percentage']:.1f}%")
    print(f"  Avg erase count: {flash['health']['avg_erase_count']:.1f}")

    print("\nWear Leveling:")
    print(f"  Blocks moved: {flash['wear_leveling']['blocks_moved']}")

    print("\nGarbage Collection:")
    print(f"  State: {flash['garbage_collection']['state']}")
    print(f"  GC runs: {flash['garbage_collection']['gc_runs']}")
    print(f"  Blocks reclaimed: {flash['garbage_collection']['blocks_reclaimed']}")


async def cleanup(client: httpx.AsyncClient, volume_id: str) -> None:
    """Cleanup demo resources."""
    print_header("CLEANUP")

    print(f"Deleting demo volume {volume_id[:8]}...")
    await client.delete(f"{API_V1}/volumes/{volume_id}")
    print("  Done")


async def main() -> None:
    """Run the demo."""
    print("\n" + "=" * 60)
    print("        PStorage Demo - Enterprise Storage System")
    print("=" * 60)

    # Check server
    print("\nChecking API server...")
    if not await check_server():
        print("ERROR: Server not running!")
        print("\nStart the server first:")
        print("  source venv/bin/activate")
        print("  uvicorn pstorage.api.main:app --reload")
        sys.exit(1)

    print("  Server is running at", BASE_URL)

    async with httpx.AsyncClient(timeout=30.0) as client:
        try:
            # Run demos
            volume_id = await demo_volumes(client)
            await demo_data_reduction(client, volume_id)
            await demo_predictive_analytics(client)
            await demo_snapshots(client, volume_id)
            await demo_flash_metrics(client)

            # Cleanup
            await cleanup(client, volume_id)

        except httpx.HTTPError as e:
            print(f"\nHTTP Error: {e}")
            sys.exit(1)

    print_header("DEMO COMPLETE")
    print("All capabilities demonstrated successfully!")
    print("\nExplore more:")
    print("  - Swagger UI: http://localhost:8000/docs")
    print("  - ReDoc: http://localhost:8000/redoc")
    print("  - GitHub: https://github.com/srikommareddi/Storage-Appliance-Architecture")


if __name__ == "__main__":
    asyncio.run(main())
