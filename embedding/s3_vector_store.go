// S3-backed VectorStore — production cold-tier vector storage.
// (Paradigm Shift §C.8)
//
// Works with AWS S3, MinIO, Cloudflare R2, any S3-API service.
// Vectors are encoded via EncodeVector (little-endian float32 stream)
// and uploaded with STANDARD_IA storage class — cold-tier embeddings
// are read rarely and the IA tier is ~40% cheaper than STANDARD.
//
// Wires identically to MemoryVectorStore. Pair with GatewayEmbedder's
// `Cold` config field to activate the lazy cold path: cache miss →
// gateway embed → cache.Set + Cold.Put.

package embedding

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config wires the adapter. Fields mirror retention/s3_cold_store.go
// so a single envvar block can configure both Sentiae cold-storage paths.
type S3Config struct {
	Endpoint  string // e.g. "s3.amazonaws.com" or "minio.local:9000"
	AccessKey string
	SecretKey string
	Bucket    string // dedicated bucket for cold embeddings
	Region    string // defaults to "us-east-1"
	UseSSL    bool
	// StorageClass overrides the default STANDARD_IA. Set to
	// "STANDARD" if access-pattern profiling shows hits hot enough
	// that retrieval cost outweighs at-rest savings.
	StorageClass string
}

// S3VectorStore implements VectorStore against an S3-compatible service.
type S3VectorStore struct {
	client       *minio.Client
	bucket       string
	storageClass string
}

// NewS3VectorStore builds the adapter. Bucket required.
func NewS3VectorStore(cfg S3Config) (*S3VectorStore, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3_vector_store: bucket required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	storageClass := cfg.StorageClass
	if storageClass == "" {
		storageClass = "STANDARD_IA"
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3_vector_store: minio.New: %w", err)
	}
	return &S3VectorStore{client: cli, bucket: cfg.Bucket, storageClass: storageClass}, nil
}

// Put encodes the vector + uploads as an S3 object under key. Returns
// no error on overwrite — cold vectors are content-hashed upstream so
// repeats yield identical bytes.
func (s *S3VectorStore) Put(ctx context.Context, key string, v Vector) error {
	if s == nil || s.client == nil {
		return errors.New("s3_vector_store: not initialised")
	}
	body := EncodeVector(v)
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{
		ContentType:  "application/octet-stream",
		StorageClass: s.storageClass,
	})
	if err != nil {
		return fmt.Errorf("s3_vector_store: put %q: %w", key, err)
	}
	return nil
}

// Get downloads the object, decodes a Vector. Translates S3 NoSuchKey
// into ErrVectorNotFound so search-time fallback can re-embed.
func (s *S3VectorStore) Get(ctx context.Context, key string) (Vector, error) {
	if s == nil || s.client == nil {
		return nil, errors.New("s3_vector_store: not initialised")
	}
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("s3_vector_store: get %q: %w", key, err)
	}
	defer obj.Close()
	body, err := io.ReadAll(obj)
	if err != nil {
		// minio returns the head error here when the key is missing.
		var minioErr minio.ErrorResponse
		if errors.As(err, &minioErr) && minioErr.Code == "NoSuchKey" {
			return nil, ErrVectorNotFound
		}
		return nil, fmt.Errorf("s3_vector_store: read %q: %w", key, err)
	}
	if len(body) == 0 {
		return nil, ErrVectorNotFound
	}
	return DecodeVector(body, 0)
}
