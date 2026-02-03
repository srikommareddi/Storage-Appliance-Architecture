"""Tests for volume API endpoints."""

import base64

import pytest
from fastapi.testclient import TestClient

from pstorage.api.main import app


@pytest.fixture
def client():
    """Create test client."""
    return TestClient(app)


class TestVolumeAPI:
    """Volume API tests."""

    def test_create_volume(self, client):
        """Test creating a volume."""
        response = client.post(
            "/api/v1/volumes",
            json={
                "name": "test-volume",
                "size_gb": 10,
                "description": "Test volume",
                "tags": {"env": "test"},
            },
        )

        assert response.status_code == 201
        data = response.json()

        assert data["name"] == "test-volume"
        assert data["size_bytes"] == 10 * 1024 * 1024 * 1024
        assert data["state"] == "available"
        assert data["description"] == "Test volume"
        assert data["tags"]["env"] == "test"

    def test_list_volumes(self, client):
        """Test listing volumes."""
        # Create a volume first
        client.post(
            "/api/v1/volumes",
            json={"name": "list-test", "size_gb": 5},
        )

        response = client.get("/api/v1/volumes")

        assert response.status_code == 200
        data = response.json()

        assert "volumes" in data
        assert "total" in data
        assert data["total"] >= 1

    def test_get_volume(self, client):
        """Test getting a specific volume."""
        # Create a volume
        create_response = client.post(
            "/api/v1/volumes",
            json={"name": "get-test", "size_gb": 5},
        )
        volume_id = create_response.json()["id"]

        # Get the volume
        response = client.get(f"/api/v1/volumes/{volume_id}")

        assert response.status_code == 200
        assert response.json()["id"] == volume_id

    def test_get_nonexistent_volume(self, client):
        """Test getting a non-existent volume."""
        response = client.get("/api/v1/volumes/nonexistent-id")

        assert response.status_code == 404

    def test_delete_volume(self, client):
        """Test deleting a volume."""
        # Create a volume
        create_response = client.post(
            "/api/v1/volumes",
            json={"name": "delete-test", "size_gb": 5},
        )
        volume_id = create_response.json()["id"]

        # Delete the volume
        response = client.delete(f"/api/v1/volumes/{volume_id}")

        assert response.status_code == 204

        # Verify it's gone
        get_response = client.get(f"/api/v1/volumes/{volume_id}")
        assert get_response.status_code == 404

    def test_clone_volume(self, client):
        """Test cloning a volume."""
        # Create a volume
        create_response = client.post(
            "/api/v1/volumes",
            json={"name": "clone-source", "size_gb": 5},
        )
        volume_id = create_response.json()["id"]

        # Clone the volume
        response = client.post(
            f"/api/v1/volumes/{volume_id}/clone",
            json={"name": "clone-dest", "description": "Cloned volume"},
        )

        assert response.status_code == 200
        data = response.json()

        assert data["name"] == "clone-dest"
        assert data["size_bytes"] == 5 * 1024 * 1024 * 1024

    def test_write_and_read_data(self, client):
        """Test writing and reading data."""
        # Create a volume
        create_response = client.post(
            "/api/v1/volumes",
            json={"name": "write-test", "size_gb": 1},
        )
        volume_id = create_response.json()["id"]

        # Write data
        test_data = b"Hello, PStorage!"
        write_response = client.post(
            f"/api/v1/volumes/{volume_id}/write",
            json={
                "offset": 0,
                "data": base64.b64encode(test_data).decode(),
            },
        )

        assert write_response.status_code == 200
        write_result = write_response.json()
        assert write_result["size"] == len(test_data)

        # Read data
        read_response = client.get(
            f"/api/v1/volumes/{volume_id}/read",
            params={"offset": 0},
        )

        assert read_response.status_code == 200
        read_result = read_response.json()
        assert base64.b64decode(read_result["data"]) == test_data
