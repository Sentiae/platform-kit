// S3-compatible ColdStore adapter. Works with AWS S3, MinIO, R2,
// any S3-API service. Production wires this in place of the
// MemoryColdStore that ships in tests + dev.
package retention

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config wires the adapter.
type S3Config struct {
	Endpoint    string // e.g. "s3.amazonaws.com" or "minio.local:9000"
	AccessKey   string
	SecretKey   string
	Bucket      string
	Region      string // optional, defaults to "us-east-1"
	UseSSL      bool
}

// S3ColdStore is a ColdStore backed by an S3-compatible service.
type S3ColdStore struct {
	client *minio.Client
	bucket string
}

// NewS3ColdStore builds the adapter.
func NewS3ColdStore(cfg S3Config) (*S3ColdStore, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3_cold_store: bucket required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3_cold_store: minio.New: %w", err)
	}
	return &S3ColdStore{client: cli, bucket: cfg.Bucket}, nil
}

// Put uploads the bytes under `key`. Returns the s3:// URI on success.
func (s *S3ColdStore) Put(ctx context.Context, key string, body io.Reader) (string, error) {
	buf, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	_, err = s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(buf), int64(len(buf)), minio.PutObjectOptions{
		ContentType:  "application/octet-stream",
		StorageClass: "STANDARD_IA", // cheaper than STANDARD; cold tier
	})
	if err != nil {
		return "", fmt.Errorf("s3_cold_store: put: %w", err)
	}
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

// Get downloads the object.
func (s *S3ColdStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3_cold_store: get: %w", err)
	}
	return obj, nil
}

// Delete removes the object. Idempotent.
func (s *S3ColdStore) Delete(ctx context.Context, key string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("s3_cold_store: delete: %w", err)
	}
	return nil
}

var _ ColdStore = (*S3ColdStore)(nil)
