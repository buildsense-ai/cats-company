package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

const checksumScript = "#!/bin/sh\nset -eu\numask 077\n/usr/bin/sha256sum -- input.bin > sha256.txt\n"

var jobIDPattern = regexp.MustCompile(`^[0-9]+$`)

type jobResult struct {
	Text        string `json:"text"`
	SlurmJobID  string `json:"slurm_job_id"`
	InputSHA256 string `json:"input_sha256"`
	ArtifactRef string `json:"artifact_ref"`
}

func (p *pilot) submit(ctx context.Context, ref fileRef) (jobResult, error) {
	// Keep the claim on the shared filesystem so a retried worker cannot
	// submit another job if the first response or process is lost.
	claim := filepath.Join(p.shared, fmt.Sprintf("%d-%s", p.uid, p.runID))
	resultPath := filepath.Join(claim, "result.json")
	if err := os.Mkdir(claim, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return jobResult{}, errors.New("create Slurm run claim")
		}
		var result jobResult
		data, err := os.ReadFile(resultPath)
		if err == nil && json.Unmarshal(data, &result) == nil && result.InputSHA256 == ref.SHA256 {
			return result, nil
		}
		return jobResult{}, errors.New("run already claimed; check Slurm before starting a replacement")
	}

	work, err := os.MkdirTemp(claim, "work-")
	if err != nil {
		return jobResult{}, errors.New("create shared job directory")
	}
	defer os.RemoveAll(work)
	if err := p.stageInput(ctx, ref, filepath.Join(work, "input.bin")); err != nil {
		return jobResult{}, err
	}
	jobID, err := p.runChecksum(ctx, work)
	if err != nil {
		return jobResult{}, err
	}
	checksum, err := os.ReadFile(filepath.Join(work, "sha256.txt"))
	if err != nil || string(checksum) != ref.SHA256+"  input.bin\n" {
		return jobResult{}, errors.New("Slurm checksum output did not match the staged input")
	}
	digest := sha256.Sum256(checksum)
	artifact := fileRef{Kind: "result", UID: p.uid, RunID: p.runID, Size: int64(len(checksum)), SHA256: hex.EncodeToString(digest[:]), Expires: time.Now().Add(artifactTTL).Unix()}
	_, err = p.storage.PutObject(ctx, p.bucket, artifact.key(), bytes.NewReader(checksum), artifact.Size, minio.PutObjectOptions{ContentType: "text/plain"})
	if err != nil {
		return jobResult{}, errors.New("write Slurm result to object storage")
	}
	result := jobResult{
		Text:        "Slurm 已完成输入文件 SHA-256 校验。",
		SlurmJobID:  jobID,
		InputSHA256: ref.SHA256,
		ArtifactRef: p.sealRef(artifact),
	}
	encoded, _ := json.Marshal(result)
	tmpPath := filepath.Join(claim, "result.tmp")
	if err := os.WriteFile(tmpPath, encoded, 0o600); err != nil {
		return jobResult{}, errors.New("persist Slurm result receipt")
	}
	if err := os.Rename(tmpPath, resultPath); err != nil {
		return jobResult{}, errors.New("publish Slurm result receipt")
	}
	return result, nil
}

func (p *pilot) runChecksum(ctx context.Context, work string) (string, error) {
	name := "cats-file-" + p.runID
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sbatch", "--parsable", "--wait", "--no-requeue",
		"--job-name="+name, "--partition="+p.partition, "--account="+p.account,
		"--nodes=1", "--ntasks=1", "--cpus-per-task=1", "--mem=64M", "--time=1", "--deadline=now+3minutes", "--export=NIL",
		"--chdir="+work, "--output="+filepath.Join(work, "slurm.out"), "--error="+filepath.Join(work, "slurm.err"))
	cmd.Dir = work
	cmd.Env = slurmEnv()
	cmd.Stdin = strings.NewReader(checksumScript)
	cmd.WaitDelay = 3 * time.Second
	stdout, err := cmd.Output()
	if ctx.Err() != nil {
		// sbatch can be interrupted after Slurm accepted the request and before
		// we read its id. The Cats run gives the job a unique name for cleanup.
		cleanup, stop := context.WithTimeout(context.Background(), 8*time.Second)
		defer stop()
		scancel := exec.CommandContext(cleanup, "scancel", "--name="+name)
		scancel.Env = slurmEnv()
		if err := scancel.Run(); err != nil {
			return "", errors.New("Slurm wait expired; could not confirm cancellation")
		}
		return "", errors.New("Slurm wait expired; cancellation requested")
	}
	jobID, _, _ := strings.Cut(strings.TrimSpace(string(stdout)), ";")
	if jobIDPattern.MatchString(jobID) {
		if err != nil {
			return "", fmt.Errorf("Slurm job %s failed", jobID)
		}
		return jobID, nil
	}
	return "", errors.New("Slurm submission did not return a job id; check the queue before retrying")
}

func slurmEnv() []string {
	env := []string{"LC_ALL=C"}
	for _, name := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TZ", "SLURM_CONF", "SLURM_CONF_SERVER", "LD_LIBRARY_PATH"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}
