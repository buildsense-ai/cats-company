package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
)

func main() {
	var endpoint string
	var region string
	var bucket string
	var key string
	var output string

	flag.StringVar(&endpoint, "endpoint", "", "TOS endpoint")
	flag.StringVar(&region, "region", "", "TOS region")
	flag.StringVar(&bucket, "bucket", "", "TOS bucket")
	flag.StringVar(&key, "key", "", "TOS object key")
	flag.StringVar(&output, "output", "", "local destination")
	flag.Parse()

	if err := fetch(endpoint, region, bucket, key, output); err != nil {
		fmt.Fprintf(os.Stderr, "tos-fetch: %v\n", err)
		os.Exit(1)
	}
}

func fetch(endpoint, region, bucket, key, output string) error {
	accessKey := strings.TrimSpace(os.Getenv("CATSCO_WORKER_ARTIFACT_ACCESS_KEY_ID"))
	secretKey := strings.TrimSpace(os.Getenv("CATSCO_WORKER_ARTIFACT_SECRET_ACCESS_KEY"))
	securityToken := strings.TrimSpace(os.Getenv("CATSCO_WORKER_ARTIFACT_SECURITY_TOKEN"))
	if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(region) == "" ||
		strings.TrimSpace(bucket) == "" || strings.TrimSpace(key) == "" || strings.TrimSpace(output) == "" {
		return errors.New("endpoint, region, bucket, key and output are required")
	}
	if accessKey == "" || secretKey == "" {
		return errors.New("private worker artifact credentials are not configured")
	}

	client, err := tos.NewClientV2(endpoint,
		tos.WithRegion(region),
		tos.WithCredentialsProvider(tos.NewStaticCredentialsProvider(accessKey, secretKey, securityToken)),
		tos.WithConnectionTimeout(15*time.Second),
		tos.WithRequestTimeout(15*time.Minute),
		tos.WithMaxRetryCount(4),
	)
	if err != nil {
		return fmt.Errorf("create TOS client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if _, err := client.GetObjectToFile(ctx, &tos.GetObjectToFileInput{
		GetObjectV2Input: tos.GetObjectV2Input{Bucket: bucket, Key: key},
		FilePath:         output,
	}); err != nil {
		return fmt.Errorf("download private object: %w", err)
	}
	if err := os.Chmod(output, 0o600); err != nil {
		return fmt.Errorf("protect downloaded object: %w", err)
	}
	return nil
}
