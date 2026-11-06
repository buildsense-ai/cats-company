package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	zosUploadAttempts = 3
	zosUploadPrefix   = "uploads/"
)

type UploadObjectInfo struct {
	Size int64
}

// UploadStorage is the single storage boundary for user-uploaded objects.
// Put must not return success until the supplied bytes are durably readable.
type UploadStorage interface {
	Remote() bool
	LocalFallbackEnabled() bool
	Put(ctx context.Context, key, sourcePath string, size int64, sha256Hex, contentType string) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Stat(ctx context.Context, key string) (UploadObjectInfo, error)
}

type localUploadStorage struct {
	baseDir string
}

func NewLocalUploadStorage(baseDir string) UploadStorage {
	return &localUploadStorage{baseDir: baseDir}
}

func (s *localUploadStorage) Remote() bool               { return false }
func (s *localUploadStorage) LocalFallbackEnabled() bool { return false }

func (s *localUploadStorage) Put(_ context.Context, key, sourcePath string, size int64, _ string, _ string) error {
	dst, err := localUploadPath(s.baseDir, key)
	if err != nil {
		return err
	}
	if err := ensureLocalUploadParent(s.baseDir, dst); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if err := ensureLocalUploadParent(s.baseDir, dst); err != nil {
		return err
	}
	if err := copyFileAtomically(sourcePath, dst, size); err != nil {
		return fmt.Errorf("persist local upload: %w", err)
	}
	return nil
}

func (s *localUploadStorage) Open(_ context.Context, key string) (io.ReadCloser, error) {
	path, err := localUploadPath(s.baseDir, key)
	if err != nil {
		return nil, err
	}
	if err := ensureLocalUploadParent(s.baseDir, path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("upload is a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("upload is not a regular file")
	}
	return os.Open(path)
}

func (s *localUploadStorage) Stat(_ context.Context, key string) (UploadObjectInfo, error) {
	path, err := localUploadPath(s.baseDir, key)
	if err != nil {
		return UploadObjectInfo{}, err
	}
	if err := ensureLocalUploadParent(s.baseDir, path); err != nil {
		return UploadObjectInfo{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return UploadObjectInfo{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return UploadObjectInfo{}, errors.New("upload is a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return UploadObjectInfo{}, errors.New("upload is not a regular file")
	}
	return UploadObjectInfo{Size: info.Size()}, nil
}

type zosUploadConfig struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	SecurityToken   string
	LocalFallback   bool
}

type zosUploadStorage struct {
	client        *minio.Client
	bucket        string
	localFallback bool
}

func NewUploadStorageFromEnv(baseDir string) (UploadStorage, error) {
	backend := strings.ToLower(strings.TrimSpace(os.Getenv("CATSCO_UPLOAD_STORAGE")))
	if backend == "" || backend == "local" {
		return NewLocalUploadStorage(baseDir), nil
	}
	if backend != "zos" {
		return nil, fmt.Errorf("unsupported CATSCO_UPLOAD_STORAGE %q (expected local or zos)", backend)
	}
	localFallback, err := parseLocalUploadFallback(os.Getenv("CATSCO_UPLOAD_LOCAL_FALLBACK"))
	if err != nil {
		return nil, err
	}
	return newZOSUploadStorage(zosUploadConfig{
		Endpoint:        strings.TrimSpace(os.Getenv("CATSCO_UPLOAD_ZOS_ENDPOINT")),
		Region:          strings.TrimSpace(os.Getenv("CATSCO_UPLOAD_ZOS_REGION")),
		Bucket:          strings.TrimSpace(os.Getenv("CATSCO_UPLOAD_ZOS_BUCKET")),
		AccessKeyID:     strings.TrimSpace(os.Getenv("CATSCO_UPLOAD_ZOS_ACCESS_KEY_ID")),
		SecretAccessKey: strings.TrimSpace(os.Getenv("CATSCO_UPLOAD_ZOS_SECRET_ACCESS_KEY")),
		SecurityToken:   strings.TrimSpace(os.Getenv("CATSCO_UPLOAD_ZOS_SECURITY_TOKEN")),
		LocalFallback:   localFallback,
	})
}

func parseLocalUploadFallback(raw string) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return true, nil
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, errors.New("CATSCO_UPLOAD_LOCAL_FALLBACK must be true or false")
	}
	return enabled, nil
}

func newZOSUploadStorage(cfg zosUploadConfig) (UploadStorage, error) {
	if cfg.Endpoint == "" || cfg.Region == "" || cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("CATSCO_UPLOAD_STORAGE=zos requires ZOS endpoint, region, bucket, access key and secret key")
	}
	endpoint, secure, err := normalizeS3Endpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SecurityToken),
		Secure:       secure,
		Region:       cfg.Region,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, fmt.Errorf("create CTyun ZOS client: %w", err)
	}
	return &zosUploadStorage{client: client, bucket: cfg.Bucket, localFallback: cfg.LocalFallback}, nil
}

func normalizeS3Endpoint(raw string) (string, bool, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", false, errors.New("CATSCO_UPLOAD_ZOS_ENDPOINT must be an HTTP(S) origin without a path")
	}
	return parsed.Host, parsed.Scheme == "https", nil
}

func (s *zosUploadStorage) Remote() bool               { return true }
func (s *zosUploadStorage) LocalFallbackEnabled() bool { return s.localFallback }

func (s *zosUploadStorage) Put(ctx context.Context, key, sourcePath string, size int64, sha256Hex, contentType string) error {
	objectKey, err := normalizeUploadObjectKey(key)
	if err != nil {
		return err
	}
	if size < 0 || len(sha256Hex) != sha256.Size*2 {
		return errors.New("invalid upload size or SHA-256")
	}

	var lastErr error
	for attempt := 1; attempt <= zosUploadAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, openErr := os.Open(sourcePath)
		if openErr != nil {
			return fmt.Errorf("open staged upload: %w", openErr)
		}
		_, putErr := s.client.PutObject(ctx, s.bucket, objectKey, source, size, minio.PutObjectOptions{
			ContentType:  contentType,
			UserMetadata: map[string]string{"sha256": sha256Hex},
		})
		closeErr := source.Close()
		if putErr != nil {
			lastErr = fmt.Errorf("put object attempt %d: %w", attempt, putErr)
		} else if closeErr != nil {
			lastErr = fmt.Errorf("close staged upload: %w", closeErr)
		} else if verifyErr := s.verify(ctx, objectKey, size, sha256Hex); verifyErr == nil {
			return nil
		} else {
			lastErr = fmt.Errorf("verify object attempt %d: %w", attempt, verifyErr)
		}
		if attempt < zosUploadAttempts {
			timer := time.NewTimer(time.Duration(attempt) * 250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return fmt.Errorf("CTyun ZOS upload failed after %d attempts: %w", zosUploadAttempts, lastErr)
}

func (s *zosUploadStorage) verify(ctx context.Context, key string, wantSize int64, wantSHA256 string) error {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return err
	}
	if info.Size != wantSize {
		return fmt.Errorf("object size is %d, want %d", info.Size, wantSize)
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return err
	}
	defer object.Close()
	hash := sha256.New()
	gotSize, err := io.Copy(hash, io.LimitReader(object, wantSize+1))
	if err != nil {
		return err
	}
	if gotSize != wantSize {
		return fmt.Errorf("downloaded object size is %d, want %d", gotSize, wantSize)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != wantSHA256 {
		return errors.New("downloaded object SHA-256 does not match staged upload")
	}
	return nil
}

func (s *zosUploadStorage) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	objectKey, err := normalizeUploadObjectKey(key)
	if err != nil {
		return nil, err
	}
	return s.client.GetObject(ctx, s.bucket, objectKey, minio.GetObjectOptions{})
}

func (s *zosUploadStorage) Stat(ctx context.Context, key string) (UploadObjectInfo, error) {
	objectKey, err := normalizeUploadObjectKey(key)
	if err != nil {
		return UploadObjectInfo{}, err
	}
	info, err := s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		return UploadObjectInfo{}, err
	}
	return UploadObjectInfo{Size: info.Size}, nil
}

func isUploadNotFound(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	response := minio.ToErrorResponse(err)
	return response.StatusCode == 404 || response.Code == "NoSuchKey" || response.Code == "NoSuchObject"
}

func normalizeUploadObjectKey(key string) (string, error) {
	key = strings.TrimPrefix(strings.TrimSpace(strings.ReplaceAll(key, "\\", "/")), "/")
	if !strings.HasPrefix(key, zosUploadPrefix) || strings.Contains(key, "..") || strings.ContainsAny(key, "\r\n\x00") {
		return "", errors.New("invalid upload object key")
	}
	return key, nil
}

func localUploadPath(baseDir, key string) (string, error) {
	key, err := normalizeUploadObjectKey(key)
	if err != nil {
		return "", err
	}
	rel := filepath.FromSlash(strings.TrimPrefix(key, zosUploadPrefix))
	if filepath.IsAbs(rel) || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." {
		return "", errors.New("invalid local upload path")
	}
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(base, rel))
	if err != nil || !strings.HasPrefix(full, base+string(os.PathSeparator)) {
		return "", errors.New("local upload path escapes storage root")
	}
	return full, nil
}

func uploadStagingDir() string {
	if configured := strings.TrimSpace(os.Getenv("CATSCO_UPLOAD_STAGING_DIR")); configured != "" {
		return configured
	}
	return filepath.Join(os.TempDir(), "catsco-upload-staging")
}

func saveUploadReader(ctx context.Context, storage UploadStorage, key string, source io.Reader, maxSize int64, contentType string) (int64, string, error) {
	if source == nil {
		return 0, "", errors.New("missing upload body")
	}
	stageDir := uploadStagingDir()
	if err := os.MkdirAll(stageDir, 0700); err != nil {
		return 0, "", fmt.Errorf("create upload staging directory: %w", err)
	}
	stage, err := os.CreateTemp(stageDir, "upload-*")
	if err != nil {
		return 0, "", fmt.Errorf("create upload staging file: %w", err)
	}
	stagePath := stage.Name()
	defer os.Remove(stagePath)
	written, err := io.Copy(stage, io.LimitReader(source, maxSize+1))
	if err != nil {
		_ = stage.Close()
		return 0, "", err
	}
	if written > maxSize {
		_ = stage.Close()
		return written, "", fmt.Errorf("file too large; maximum supported size is %dMB", maxUploadSizeMB)
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		return 0, "", err
	}
	if err := stage.Close(); err != nil {
		return 0, "", err
	}
	return persistStagedUpload(ctx, storage, key, stagePath, contentType)
}

func persistStagedUpload(ctx context.Context, storage UploadStorage, key, stagePath, contentType string) (int64, string, error) {
	key, err := normalizeUploadObjectKey(key)
	if err != nil {
		return 0, "", err
	}
	stage, err := os.Open(stagePath)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	size, hashErr := io.Copy(hash, stage)
	closeErr := stage.Close()
	if hashErr != nil {
		return 0, "", hashErr
	}
	if closeErr != nil {
		return 0, "", closeErr
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if storage == nil {
		return 0, "", errors.New("upload storage is not configured")
	}
	if err := storage.Put(ctx, key, stagePath, size, digest, contentType); err != nil {
		if storage.Remote() {
			log.Printf("[upload] ZOS upload failed; no persistent local copy retained key=%q err=%v", key, err)
		}
		return size, digest, err
	}
	return size, digest, nil
}

func ensureLocalUploadParent(baseDir, destinationPath string) error {
	base, err := filepath.Abs(baseDir)
	if err != nil {
		return err
	}
	parent, err := filepath.Abs(filepath.Dir(destinationPath))
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(base, parent)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return errors.New("local upload parent escapes storage root")
	}
	current := base
	for _, part := range strings.Split(relative, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("local upload parent contains a non-directory or symbolic link")
		}
	}
	return nil
}

func copyFileAtomically(sourcePath, destinationPath string, wantSize int64) error {
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0755); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	temp, err := os.CreateTemp(filepath.Dir(destinationPath), ".upload-copy-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	written, copyErr := io.Copy(temp, source)
	if copyErr != nil {
		_ = temp.Close()
		return copyErr
	}
	if written != wantSize {
		_ = temp.Close()
		return fmt.Errorf("copied %d bytes, want %d", written, wantSize)
	}
	if err := temp.Chmod(0644); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, destinationPath)
}

func openUploadWithLocalFallback(ctx context.Context, storage UploadStorage, baseDir, key string) (io.ReadCloser, error) {
	if storage != nil {
		body, err := storage.Open(ctx, key)
		if err == nil {
			return body, nil
		}
		if !storage.Remote() || !storage.LocalFallbackEnabled() {
			return nil, err
		}
		local := NewLocalUploadStorage(baseDir)
		if fallback, fallbackErr := local.Open(ctx, key); fallbackErr == nil {
			log.Printf("[upload] ZOS read failed; using local fallback key=%q err=%v", key, err)
			return fallback, nil
		}
		return nil, err
	}
	return NewLocalUploadStorage(baseDir).Open(ctx, key)
}

func statUploadWithLocalFallback(ctx context.Context, storage UploadStorage, baseDir, key string) (UploadObjectInfo, error) {
	if storage != nil {
		info, err := storage.Stat(ctx, key)
		if err == nil || !storage.Remote() || !storage.LocalFallbackEnabled() {
			return info, err
		}
		fallback, fallbackErr := NewLocalUploadStorage(baseDir).Stat(ctx, key)
		if fallbackErr == nil {
			log.Printf("[upload] ZOS stat failed; using local fallback key=%q err=%v", key, err)
			return fallback, nil
		}
		return UploadObjectInfo{}, err
	}
	return NewLocalUploadStorage(baseDir).Stat(ctx, key)
}

func materializeUpload(ctx context.Context, storage UploadStorage, baseDir, key string, maxSize int64) (string, func(), error) {
	if storage == nil {
		storage = NewLocalUploadStorage(baseDir)
	}
	path, cleanup, err := materializeUploadFrom(ctx, storage, key, maxSize)
	if err == nil {
		return path, cleanup, nil
	}
	if !storage.Remote() || !storage.LocalFallbackEnabled() {
		return "", func() {}, err
	}
	fallback, fallbackCleanup, fallbackErr := materializeUploadFrom(ctx, NewLocalUploadStorage(baseDir), key, maxSize)
	if fallbackErr == nil {
		log.Printf("[upload] ZOS read failed; using local fallback key=%q err=%v", key, err)
		return fallback, fallbackCleanup, nil
	}
	return "", func() {}, err
}

func materializeUploadFrom(ctx context.Context, storage UploadStorage, key string, maxSize int64) (string, func(), error) {
	body, err := storage.Open(ctx, key)
	if err != nil {
		return "", func() {}, err
	}
	defer body.Close()
	if err := os.MkdirAll(uploadStagingDir(), 0700); err != nil {
		return "", func() {}, err
	}
	file, err := os.CreateTemp(uploadStagingDir(), "read-*")
	if err != nil {
		return "", func() {}, err
	}
	path := file.Name()
	written, err := io.Copy(file, io.LimitReader(body, maxSize+1))
	if err == nil && written > maxSize {
		err = fmt.Errorf("upload exceeds read limit of %d bytes", maxSize)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", func() {}, err
	}
	return path, func() { _ = os.Remove(path) }, nil
}
