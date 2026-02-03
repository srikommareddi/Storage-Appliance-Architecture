# PStorage Docker Image
FROM python:3.11-slim

# Set environment variables
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1

# Set work directory
WORKDIR /app

# Install system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    && rm -rf /var/lib/apt/lists/*

# Copy requirements first for caching
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy source code
COPY src/ ./src/
COPY pyproject.toml .

# Install the package
RUN pip install --no-cache-dir -e .

# Create data directories
RUN mkdir -p /data/pstorage /metadata/pstorage

# Set environment variables for storage
ENV PSTORAGE_DATA_DIR=/data/pstorage \
    PSTORAGE_METADATA_DIR=/metadata/pstorage \
    PSTORAGE_API_HOST=0.0.0.0 \
    PSTORAGE_API_PORT=8000

# Expose API port
EXPOSE 8000

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8000/health || exit 1

# Run the API server
CMD ["python", "-m", "uvicorn", "pstorage.api.main:app", "--host", "0.0.0.0", "--port", "8000"]
