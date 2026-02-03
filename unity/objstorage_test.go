package unity

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
)

// Test Data Domain Deduplication Engine
func TestDataDomainDeduplication(t *testing.T) {
	engine := NewDataDomainEngine(DedupModeInline)
	ctx := context.Background()

	t.Run("Basic deduplication and reconstruction", func(t *testing.T) {
		data := bytes.Repeat([]byte("test data block "), 100)

		recipe, err := engine.DeduplicateAndStore(ctx, "object1", data)
		if err != nil {
			t.Fatalf("DeduplicateAndStore failed: %v", err)
		}

		if recipe == nil {
			t.Fatal("Recipe should not be nil")
		}

		// Reconstruct data
		reconstructed, err := engine.ReconstructData(ctx, recipe)
		if err != nil {
			t.Fatalf("ReconstructData failed: %v", err)
		}

		if !bytes.Equal(reconstructed, data) {
			t.Error("Reconstructed data doesn't match original")
		}
	})

	t.Run("Deduplication statistics", func(t *testing.T) {
		data := bytes.Repeat([]byte("stats test "), 100)

		_, err := engine.DeduplicateAndStore(ctx, "stats-object", data)
		if err != nil {
			t.Fatalf("DeduplicateAndStore failed: %v", err)
		}

		stats := engine.GetStats()

		// Verify stats structure is populated
		if stats.TotalDataIngested > 0 {
			t.Logf("Total data ingested: %d bytes", stats.TotalDataIngested)
		}
		if stats.TotalSegments > 0 {
			t.Logf("Total segments: %d", stats.TotalSegments)
		}
		if stats.DedupRatio > 0 {
			t.Logf("Dedup ratio: %.2f", stats.DedupRatio)
		}
	})
}

// Test Object Protocol Engine
func TestObjectProtocolEngine(t *testing.T) {
	pool := NewUnifiedStoragePool()
	engine := NewObjectProtocolEngine(pool)

	t.Run("Create bucket", func(t *testing.T) {
		bucketName := "test-bucket"
		config := BucketConfig{
			Owner:             "test-user",
			VersioningEnabled: true,
			DefaultTier:       StorageTierStandard,
		}

		err := engine.CreateBucket(bucketName, config)
		if err != nil {
			t.Fatalf("CreateBucket failed: %v", err)
		}

		// Verify bucket exists by trying to create it again (should fail)
		err = engine.CreateBucket(bucketName, config)
		if err == nil {
			t.Error("Creating duplicate bucket should fail")
		}
	})

	t.Run("Put and Get object", func(t *testing.T) {
		ctx := context.Background()
		bucketName := "object-test-bucket"
		objectKey := "test/object.txt"

		// Create bucket first
		config := BucketConfig{Owner: "test-user"}
		engine.CreateBucket(bucketName, config)

		// Put object
		data := []byte("test object content")
		metadata := map[string]string{
			"Content-Type": "text/plain",
		}

		objMeta, err := engine.PutObject(ctx, bucketName, objectKey, bytes.NewReader(data), int64(len(data)), metadata)
		if err != nil {
			t.Fatalf("PutObject failed: %v", err)
		}
		if objMeta.Key != objectKey {
			t.Error("Object key mismatch")
		}

		// Get object
		reader, retrievedMeta, err := engine.GetObject(ctx, bucketName, objectKey, "")
		if err != nil {
			t.Fatalf("GetObject failed: %v", err)
		}
		defer reader.Close()

		retrievedData, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("Failed to read object data: %v", err)
		}

		if !bytes.Equal(retrievedData, data) {
			t.Error("Retrieved data doesn't match original")
		}
		if retrievedMeta.Key != objectKey {
			t.Error("Retrieved metadata key mismatch")
		}
	})

	t.Run("Delete object", func(t *testing.T) {
		ctx := context.Background()
		bucketName := "delete-test-bucket"
		objectKey := "delete-me.txt"

		config := BucketConfig{Owner: "test-user"}
		engine.CreateBucket(bucketName, config)

		// Put object
		data := []byte("temporary data")
		engine.PutObject(ctx, bucketName, objectKey, bytes.NewReader(data), int64(len(data)), nil)

		// Delete object
		err := engine.DeleteObject(ctx, bucketName, objectKey, "")
		if err != nil {
			t.Fatalf("DeleteObject failed: %v", err)
		}

		// Try to get deleted object - should fail
		_, _, err = engine.GetObject(ctx, bucketName, objectKey, "")
		if err == nil {
			t.Error("Expected error when getting deleted object")
		}
	})

	t.Run("Multipart upload", func(t *testing.T) {
		ctx := context.Background()
		bucketName := "multipart-bucket"
		objectKey := "large-object.bin"

		config := BucketConfig{Owner: "test-user"}
		engine.CreateBucket(bucketName, config)

		// Initiate multipart upload
		uploadID, err := engine.InitiateMultipartUpload(ctx, bucketName, objectKey)
		if err != nil {
			t.Fatalf("InitiateMultipartUpload failed: %v", err)
		}

		if uploadID == "" {
			t.Fatal("Upload ID should not be empty")
		}

		// Upload parts
		part1 := bytes.Repeat([]byte("part1"), 1000)
		partInfo1, err := engine.UploadPart(ctx, bucketName, objectKey, uploadID, 1, bytes.NewReader(part1), int64(len(part1)))
		if err != nil {
			t.Fatalf("UploadPart 1 failed: %v", err)
		}

		part2 := bytes.Repeat([]byte("part2"), 1000)
		partInfo2, err := engine.UploadPart(ctx, bucketName, objectKey, uploadID, 2, bytes.NewReader(part2), int64(len(part2)))
		if err != nil {
			t.Fatalf("UploadPart 2 failed: %v", err)
		}

		// Complete multipart upload
		parts := []PartInfo{*partInfo1, *partInfo2}

		objMeta, err := engine.CompleteMultipartUpload(ctx, bucketName, objectKey, uploadID, parts)
		if err != nil {
			t.Fatalf("CompleteMultipartUpload failed: %v", err)
		}

		if objMeta.Key != objectKey {
			t.Error("Completed object key mismatch")
		}
	})
}

// Benchmark tests
func BenchmarkDeduplication(b *testing.B) {
	engine := NewDataDomainEngine(DedupModeInline)
	data := bytes.Repeat([]byte("benchmark data block "), 1000)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		objectID := fmt.Sprintf("bench-object-%d", i)
		engine.DeduplicateAndStore(ctx, objectID, data)
	}
}

func BenchmarkObjectPut(b *testing.B) {
	pool := NewUnifiedStoragePool()
	engine := NewObjectProtocolEngine(pool)
	bucketName := "bench-bucket"
	config := BucketConfig{Owner: "bench-user"}
	engine.CreateBucket(bucketName, config)

	data := bytes.Repeat([]byte("test"), 1024) // 4KB object
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("object-%d", i)
		engine.PutObject(ctx, bucketName, key, bytes.NewReader(data), int64(len(data)), nil)
	}
}

func BenchmarkObjectGet(b *testing.B) {
	pool := NewUnifiedStoragePool()
	engine := NewObjectProtocolEngine(pool)
	bucketName := "bench-bucket"
	config := BucketConfig{Owner: "bench-user"}
	engine.CreateBucket(bucketName, config)

	data := bytes.Repeat([]byte("test"), 1024)
	ctx := context.Background()
	engine.PutObject(ctx, bucketName, "test-object", bytes.NewReader(data), int64(len(data)), nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader, _, err := engine.GetObject(ctx, bucketName, "test-object", "")
		if err != nil {
			b.Fatal(err)
		}
		io.ReadAll(reader)
		reader.Close()
	}
}

func BenchmarkParallelObjectPut(b *testing.B) {
	pool := NewUnifiedStoragePool()
	engine := NewObjectProtocolEngine(pool)
	bucketName := "bench-parallel-bucket"
	config := BucketConfig{Owner: "bench-user"}
	engine.CreateBucket(bucketName, config)

	data := bytes.Repeat([]byte("test"), 1024)
	ctx := context.Background()

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := fmt.Sprintf("object-%d", i)
			engine.PutObject(ctx, bucketName, key, bytes.NewReader(data), int64(len(data)), nil)
			i++
		}
	})
}

func BenchmarkDataReconstruction(b *testing.B) {
	engine := NewDataDomainEngine(DedupModePostProcess)
	ctx := context.Background()
	data := bytes.Repeat([]byte("reconstruct"), 1000)

	recipe, _ := engine.DeduplicateAndStore(ctx, "bench-recipe", data)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		engine.ReconstructData(ctx, recipe)
	}
}
