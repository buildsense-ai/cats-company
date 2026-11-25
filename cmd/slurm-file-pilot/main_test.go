package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestFileRefScope(t *testing.T) {
	p := pilot{uid: 441, secret: []byte(strings.Repeat("t", 32))}
	now := time.Now()
	ref := fileRef{Kind: "input", RunID: "run_123", UID: 441, Size: 10, SHA256: strings.Repeat("f", 64), Expires: now.Add(time.Minute).Unix()}
	token := p.sealRef(ref)
	if got, err := p.parseRef(token, "input", now); err != nil || got != ref {
		t.Fatalf("valid reference = %+v, %v", got, err)
	}
	for _, tc := range []struct {
		name  string
		uid   int64
		kind  string
		token string
		time  time.Time
	}{
		{"other account", 442, "input", token, now},
		{"other kind", 441, "result", token, now},
		{"modified signature", 441, "input", token + "a", now},
		{"expired", 441, "input", token, now.Add(time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.uid = tc.uid
			if _, err := p.parseRef(tc.token, tc.kind, tc.time); err == nil {
				t.Fatal("unexpected valid reference")
			}
		})
	}
}

func TestDirectUploadPolicyAndInputCheck(t *testing.T) {
	const contents = "upload only to object storage\n"
	sum := sha256.Sum256([]byte(contents))
	digest := hex.EncodeToString(sum[:])
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/isolated/slurm-file-pilot/441/run_prepare/input" {
			t.Errorf("unexpected storage request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", `"fake"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		_, _ = fmt.Fprint(w, contents)
	}))
	defer s3.Close()
	storage, err := minio.New(strings.TrimPrefix(s3.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4("test-access", "test-secret", ""), Region: "us-east-1", BucketLookup: minio.BucketLookupPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := pilot{uid: 441, runID: "run_prepare", secret: []byte(strings.Repeat("t", 32)), shared: t.TempDir(), storage: storage, bucket: "isolated"}
	ctx := t.Context()
	output, err := p.prepare(ctx, int64(len(contents)), digest)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(output)
	var grant struct {
		Ref    string `json:"input_ref"`
		Upload struct {
			URL    string            `json:"url"`
			Fields map[string]string `json:"fields"`
		} `json:"upload"`
	}
	if err := json.Unmarshal(encoded, &grant); err != nil || !strings.HasPrefix(grant.Upload.URL, s3.URL+"/") {
		t.Fatalf("upload grant did not use object storage: %v, %s", err, grant.Upload.URL)
	}
	policy, err := base64.StdEncoding.DecodeString(grant.Upload.Fields["policy"])
	if err != nil || !strings.Contains(string(policy), "slurm-file-pilot/441/run_prepare/input") {
		t.Fatalf("grant did not constrain the object key: %v, %s", err, policy)
	}
	var parsed struct{ Conditions []json.RawMessage }
	if err := json.Unmarshal(policy, &parsed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, condition := range parsed.Conditions {
		var bounds []any
		if json.Unmarshal(condition, &bounds) == nil && len(bounds) == 3 &&
			bounds[0] == "content-length-range" && bounds[1] == float64(len(contents)) && bounds[2] == float64(len(contents)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("grant did not constrain object size: %s", policy)
	}
	ref, err := p.parseRef(grant.Ref, "input", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "input.bin")
	if err := p.stageInput(ctx, ref, path); err != nil {
		t.Fatal(err)
	}
	staged, err := os.ReadFile(path)
	if err != nil || string(staged) != contents {
		t.Fatalf("staged input = %q, %v", staged, err)
	}
	p.runID = "run_invalid"
	ref.SHA256 = strings.Repeat("0", 64)
	if _, err := p.submit(ctx, ref); err == nil || err.Error() != "input size or SHA-256 mismatch" {
		t.Fatalf("unexpected submission result for modified input: %v", err)
	}
	if _, err := p.submit(ctx, ref); err == nil || !strings.HasPrefix(err.Error(), "run already claimed") {
		t.Fatalf("unexpected duplicate submission for ambiguous run: %v", err)
	}
}

func TestCompletedSubmissionReturnsReceipt(t *testing.T) {
	p := pilot{uid: 441, runID: "run_submit", shared: t.TempDir()}
	ref := fileRef{SHA256: strings.Repeat("d", 64)}
	want := jobResult{SlurmJobID: "42", InputSHA256: ref.SHA256, ArtifactRef: "reference"}
	claim := filepath.Join(p.shared, "441-run_submit")
	if err := os.Mkdir(claim, 0o700); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(want)
	if err := os.WriteFile(filepath.Join(claim, "result.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := p.submit(context.Background(), ref); err != nil || got != want {
		t.Fatalf("completed replay = %+v, %v", got, err)
	}
}
