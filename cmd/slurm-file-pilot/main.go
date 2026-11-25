// Command slurm-file-pilot executes the fixed SHA-256 job through service-run-worker.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	maxInputSize = 256 << 20
	uploadTTL    = 30 * time.Minute
	downloadTTL  = 5 * time.Minute
	artifactTTL  = 24 * time.Hour
)

var (
	runIDPattern  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type request struct {
	Action      string `json:"action"`
	Size        int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	InputRef    string `json:"input_ref"`
	ArtifactRef string `json:"artifact_ref"`
}

type fileRef struct {
	Kind    string `json:"kind"`
	RunID   string `json:"run_id"`
	UID     int64  `json:"uid"`
	Size    int64  `json:"size_bytes"`
	SHA256  string `json:"sha256"`
	Expires int64  `json:"expires"`
}

func (ref fileRef) key() string {
	return fmt.Sprintf("slurm-file-pilot/%d/%s/%s", ref.UID, ref.RunID, ref.Kind)
}

type pilot struct {
	storage   *minio.Client
	bucket    string
	secret    []byte
	shared    string
	partition string
	account   string
	uid       int64
	runID     string
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	p, err := pilotFromEnv()
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(os.Stdin, 8193))
	if err != nil || len(body) > 8192 {
		return errors.New("pilot request exceeds 8192 bytes or cannot be read")
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	var req request
	if err := dec.Decode(&req); err != nil || dec.Decode(new(any)) != io.EOF {
		return errors.New("invalid pilot request")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	output, err := p.execute(ctx, req)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"output": output})
}

func pilotFromEnv() (*pilot, error) {
	uid, _ := strconv.ParseInt(os.Getenv("CATSCO_CALLER_UID"), 10, 64)
	p := &pilot{
		uid:       uid,
		runID:     os.Getenv("CATSCO_RUN_ID"),
		bucket:    os.Getenv("CATSCO_SLURM_BUCKET"),
		secret:    []byte(os.Getenv("CATSCO_SLURM_REF_SECRET")),
		shared:    os.Getenv("CATSCO_SLURM_SHARED_DIR"),
		partition: os.Getenv("CATSCO_SLURM_PARTITION"),
		account:   os.Getenv("CATSCO_SLURM_ACCOUNT"),
	}
	if uid <= 0 || !runIDPattern.MatchString(p.runID) {
		return nil, errors.New("missing Cats worker run or caller identity")
	}
	if !runIDPattern.MatchString(p.partition) || !runIDPattern.MatchString(p.account) || !filepath.IsAbs(p.shared) || len(p.secret) < 32 || p.bucket == "" {
		return nil, errors.New("configure the pilot bucket, reference secret, shared directory, partition and account")
	}
	region := os.Getenv("CATSCO_SLURM_S3_REGION")
	access := os.Getenv("CATSCO_SLURM_S3_ACCESS_KEY")
	secret := os.Getenv("CATSCO_SLURM_S3_SECRET_KEY")
	endpoint, err := url.Parse(os.Getenv("CATSCO_SLURM_S3_ENDPOINT"))
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, errors.New("configure a S3 endpoint origin")
	}
	secure := endpoint.Scheme == "https"
	if !secure && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "localhost")) {
		return nil, errors.New("S3 endpoint must use HTTPS, except for local testing")
	}
	if region == "" || access == "" || secret == "" {
		return nil, errors.New("configure S3 region and pilot credentials")
	}
	p.storage, err = minio.New(endpoint.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(access, secret, os.Getenv("CATSCO_SLURM_S3_SESSION_TOKEN")),
		Secure:       secure,
		Region:       region,
		BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		return nil, errors.New("invalid pilot S3 configuration")
	}
	return p, nil
}

func (p *pilot) execute(ctx context.Context, req request) (any, error) {
	switch req.Action {
	case "prepare":
		return p.prepare(ctx, req.Size, req.SHA256)
	case "submit":
		ref, err := p.parseRef(req.InputRef, "input", time.Now())
		if err != nil {
			return nil, err
		}
		return p.submit(ctx, ref)
	case "download":
		ref, err := p.parseRef(req.ArtifactRef, "result", time.Now())
		if err != nil {
			return nil, err
		}
		return p.download(ctx, ref)
	default:
		return nil, errors.New("action must be prepare, submit or download")
	}
}

func (p *pilot) sealRef(ref fileRef) string {
	encoded, _ := json.Marshal(ref)
	return base64.RawURLEncoding.EncodeToString(encoded) + "." + base64.RawURLEncoding.EncodeToString(p.refMAC(encoded))
}

func (p *pilot) refMAC(encoded []byte) []byte {
	mac := hmac.New(sha256.New, p.secret)
	mac.Write([]byte("catsco.slurm-file-pilot.v1\x00"))
	mac.Write(encoded)
	return mac.Sum(nil)
}

func (p *pilot) parseRef(token, kind string, now time.Time) (fileRef, error) {
	invalid := errors.New("invalid, expired or unauthorized file reference")
	payload, signature, ok := strings.Cut(token, ".")
	if !ok || len(token) > 1024 {
		return fileRef{}, invalid
	}
	encoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return fileRef{}, invalid
	}
	mac, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(mac, p.refMAC(encoded)) {
		return fileRef{}, invalid
	}
	var ref fileRef
	if err := json.Unmarshal(encoded, &ref); err != nil || ref.Kind != kind || ref.UID != p.uid || ref.Expires <= now.Unix() || !runIDPattern.MatchString(ref.RunID) || !digestPattern.MatchString(ref.SHA256) || ref.Size <= 0 || ref.Size > maxInputSize {
		return fileRef{}, invalid
	}
	return ref, nil
}

func (p *pilot) prepare(ctx context.Context, size int64, digest string) (any, error) {
	if size <= 0 || size > maxInputSize || !digestPattern.MatchString(digest) {
		return nil, errors.New("prepare requires a file of 1 byte to 256 MiB and lowercase SHA-256")
	}
	expires := time.Now().UTC().Add(uploadTTL)
	ref := fileRef{Kind: "input", UID: p.uid, RunID: p.runID, Size: size, SHA256: digest, Expires: expires.Unix()}
	policy := minio.NewPostPolicy()
	if err := errors.Join(policy.SetBucket(p.bucket), policy.SetKey(ref.key()), policy.SetExpires(expires), policy.SetContentLengthRange(size, size)); err != nil {
		return nil, errors.New("build direct-upload policy")
	}
	target, fields, err := p.storage.PresignedPostPolicy(ctx, policy)
	if err != nil {
		return nil, errors.New("sign direct upload")
	}
	return map[string]any{
		"input_ref": p.sealRef(ref),
		"upload":    map[string]any{"method": "POST", "url": target.String(), "fields": fields, "expires_at": expires},
	}, nil
}

func (p *pilot) download(ctx context.Context, ref fileRef) (any, error) {
	ttl := min(downloadTTL, time.Until(time.Unix(ref.Expires, 0))).Truncate(time.Second)
	if ttl < time.Second {
		return nil, errors.New("artifact reference expired")
	}
	query := url.Values{"response-content-disposition": {`attachment; filename="sha256.txt"`}}
	target, err := p.storage.PresignedGetObject(ctx, p.bucket, ref.key(), ttl, query)
	if err != nil {
		return nil, errors.New("sign direct download")
	}
	return map[string]any{"download_url": target.String(), "sha256": ref.SHA256, "size_bytes": ref.Size}, nil
}

func (p *pilot) stageInput(ctx context.Context, ref fileRef, path string) error {
	object, err := p.storage.GetObject(ctx, p.bucket, ref.key(), minio.GetObjectOptions{})
	if err != nil {
		return errors.New("open uploaded object")
	}
	defer object.Close()
	destination, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("create Slurm input file")
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(object, ref.Size+1))
	if closeErr := destination.Close(); closeErr != nil || err != nil {
		return errors.New("stage input from object storage")
	}
	if n != ref.Size || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
		return errors.New("input size or SHA-256 mismatch")
	}
	return nil
}
