// S3 Glacier Deep Archive adapter. (§H.5)
//
// Writes audit-grade rows to a deep-archive storage class so 7-year
// enterprise retention is economical (~$1/TB/month vs ~$23/TB on
// STANDARD). Reads are async — RequestRestore submits an S3
// RestoreObject job; callers poll the eventual hot copy via the
// existing S3ColdStore once the unfreeze completes.
//
// One adapter, two valid storage classes:
//   - GLACIER (Glacier Flexible Retrieval) — minutes-to-hours restore.
//   - DEEP_ARCHIVE — 12-48h restore, lowest cost. Default.

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

// GlacierConfig wires the adapter. Mirrors S3Config but with a
// dedicated bucket convention so lifecycle policies + IAM split
// cleanly between hot/cold/glacier paths.
type GlacierConfig struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
	// StorageClass is GLACIER or DEEP_ARCHIVE. Defaults to DEEP_ARCHIVE.
	StorageClass string
	// RestoreDays is how long the unfrozen copy stays warm after a
	// successful RequestRestore. Defaults to 7.
	RestoreDays int
	// RestoreTier is "Standard" / "Bulk" / "Expedited" (Glacier only,
	// ignored on Deep Archive). Defaults to "Standard".
	RestoreTier string
}

// S3GlacierStore implements GlacierStore against an S3-compatible
// service that supports the deep-archive storage class.
type S3GlacierStore struct {
	client       *minio.Client
	bucket       string
	storageClass string
	restoreDays  int
	restoreTier  string
}

// NewS3GlacierStore builds the adapter.
func NewS3GlacierStore(cfg GlacierConfig) (*S3GlacierStore, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3_glacier_store: bucket required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	storageClass := cfg.StorageClass
	if storageClass == "" {
		storageClass = "DEEP_ARCHIVE"
	}
	restoreDays := cfg.RestoreDays
	if restoreDays <= 0 {
		restoreDays = 7
	}
	restoreTier := cfg.RestoreTier
	if restoreTier == "" {
		restoreTier = "Standard"
	}
	cli, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3_glacier_store: minio.New: %w", err)
	}
	return &S3GlacierStore{
		client:       cli,
		bucket:       cfg.Bucket,
		storageClass: storageClass,
		restoreDays:  restoreDays,
		restoreTier:  restoreTier,
	}, nil
}

// Archive writes the bytes under key with the configured deep-archive
// storage class. Returns the s3:// URI on success.
func (s *S3GlacierStore) Archive(ctx context.Context, key string, body io.Reader) (string, error) {
	if s == nil || s.client == nil {
		return "", errors.New("s3_glacier_store: not initialised")
	}
	buf, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	_, err = s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(buf), int64(len(buf)), minio.PutObjectOptions{
		ContentType:  "application/octet-stream",
		StorageClass: s.storageClass,
	})
	if err != nil {
		return "", fmt.Errorf("s3_glacier_store: put: %w", err)
	}
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

// RequestRestore submits an S3 RestoreObject request. The actual
// unfreeze is async — Deep Archive takes 12-48h, Glacier minutes
// to hours depending on tier. The returned jobID is the request key
// the caller can pass back to GetRestoreStatus to poll.
//
// minio-go ≥7 exposes RestoreObject; older versions fall back to
// the StatObject + retry pattern. We use the modern API.
func (s *S3GlacierStore) RequestRestore(ctx context.Context, key string) (string, error) {
	if s == nil || s.client == nil {
		return "", errors.New("s3_glacier_store: not initialised")
	}
	req := minio.RestoreRequest{}
	req.SetDays(s.restoreDays)
	if s.storageClass == "GLACIER" {
		req.SetGlacierJobParameters(minio.GlacierJobParameters{Tier: minio.TierType(s.restoreTier)})
	}
	if err := s.client.RestoreObject(ctx, s.bucket, key, "", req); err != nil {
		return "", fmt.Errorf("s3_glacier_store: restore: %w", err)
	}
	return key, nil
}

// GetRestoreStatus reports whether a previously-requested restore
// has completed. Implementations of this poll the ongoing-request
// metadata that minio surfaces on StatObject.
func (s *S3GlacierStore) GetRestoreStatus(ctx context.Context, key string) (RestoreStatus, error) {
	if s == nil || s.client == nil {
		return RestoreStatusUnknown, errors.New("s3_glacier_store: not initialised")
	}
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return RestoreStatusUnknown, fmt.Errorf("s3_glacier_store: stat: %w", err)
	}
	if info.Restore == nil {
		return RestoreStatusFrozen, nil
	}
	if info.Restore.OngoingRestore {
		return RestoreStatusPending, nil
	}
	return RestoreStatusReady, nil
}

// RestoreStatus enumerates the lifecycle of a glacier-thaw request.
type RestoreStatus string

const (
	// RestoreStatusFrozen means no restore has been requested.
	RestoreStatusFrozen RestoreStatus = "frozen"
	// RestoreStatusPending means a restore is in flight.
	RestoreStatusPending RestoreStatus = "pending"
	// RestoreStatusReady means the restored copy is hot + readable.
	RestoreStatusReady RestoreStatus = "ready"
	// RestoreStatusUnknown means the call failed; caller should retry.
	RestoreStatusUnknown RestoreStatus = "unknown"
)

// Compile-time guarantee.
var _ GlacierStore = (*S3GlacierStore)(nil)
