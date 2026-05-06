package embedding

import (
	"context"
	"testing"
)

// S3VectorStore lives behind a real S3 endpoint at runtime; a full
// integration test belongs in test/integration with testcontainers.
// These cases pin the constructor + config defaults so future drift
// surfaces immediately.

func TestS3VectorStore_NewRequiresBucket(t *testing.T) {
	_, err := NewS3VectorStore(S3Config{
		Endpoint: "s3.amazonaws.com",
	})
	if err == nil {
		t.Fatal("expected error when bucket missing")
	}
}

func TestS3VectorStore_DefaultsRegionAndStorageClass(t *testing.T) {
	s, err := NewS3VectorStore(S3Config{
		Endpoint: "s3.amazonaws.com",
		Bucket:   "embeddings-cold",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.bucket != "embeddings-cold" {
		t.Errorf("bucket %s", s.bucket)
	}
	if s.storageClass != "STANDARD_IA" {
		t.Errorf("default storage class %q", s.storageClass)
	}
}

func TestS3VectorStore_CustomStorageClass(t *testing.T) {
	s, err := NewS3VectorStore(S3Config{
		Endpoint:     "s3.amazonaws.com",
		Bucket:       "embeddings-cold",
		StorageClass: "GLACIER_IR",
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.storageClass != "GLACIER_IR" {
		t.Errorf("storage class override lost: %q", s.storageClass)
	}
}

func TestS3VectorStore_NilGuard(t *testing.T) {
	var s *S3VectorStore
	ctx := context.TODO()
	if err := s.Put(ctx, "k", Vector{1}); err == nil {
		t.Error("expected nil-receiver error on Put")
	}
	if _, err := s.Get(ctx, "k"); err == nil {
		t.Error("expected nil-receiver error on Get")
	}
}
