package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
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
	var listPrefix string
	var output string

	flag.StringVar(&endpoint, "endpoint", "", "TOS endpoint")
	flag.StringVar(&region, "region", "", "TOS region")
	flag.StringVar(&bucket, "bucket", "", "TOS bucket")
	flag.StringVar(&key, "key", "", "TOS object key")
	flag.StringVar(&listPrefix, "list-prefix", "", "list TOS object keys below this prefix")
	flag.StringVar(&output, "output", "", "local destination")
	flag.Parse()

	var err error
	if strings.TrimSpace(listPrefix) != "" {
		err = listObjects(endpoint, region, bucket, listPrefix, os.Stdout)
	} else {
		err = fetch(endpoint, region, bucket, key, output)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "tos-fetch: %v\n", err)
		os.Exit(1)
	}
}

func fetch(endpoint, region, bucket, key, output string) error {
	if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(region) == "" ||
		strings.TrimSpace(bucket) == "" || strings.TrimSpace(key) == "" || strings.TrimSpace(output) == "" {
		return errors.New("endpoint, region, bucket, key and output are required")
	}

	client, err := newClient(endpoint, region)
	if err != nil {
		return err
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

func listObjects(endpoint, region, bucket, prefix string, dst io.Writer) error {
	if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(region) == "" ||
		strings.TrimSpace(bucket) == "" || strings.TrimSpace(prefix) == "" || dst == nil {
		return errors.New("endpoint, region, bucket, list prefix and destination are required")
	}
	client, err := newClient(endpoint, region)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := client.ListObjectsType2(ctx, &tos.ListObjectsType2Input{
		Bucket:  bucket,
		Prefix:  prefix,
		MaxKeys: 1000,
	})
	if err != nil {
		return fmt.Errorf("list private objects: %w", err)
	}
	for _, object := range result.Contents {
		if strings.ContainsAny(object.Key, "\r\n\t") {
			continue
		}
		if _, err := fmt.Fprintf(dst, "%s\t%d\n", object.Key, object.LastModified.Unix()); err != nil {
			return fmt.Errorf("write object listing: %w", err)
		}
	}
	return nil
}

func newClient(endpoint, region string) (*tos.ClientV2, error) {
	accessKey := strings.TrimSpace(os.Getenv("CATSCO_WORKER_ARTIFACT_ACCESS_KEY_ID"))
	secretKey := strings.TrimSpace(os.Getenv("CATSCO_WORKER_ARTIFACT_SECRET_ACCESS_KEY"))
	securityToken := strings.TrimSpace(os.Getenv("CATSCO_WORKER_ARTIFACT_SECURITY_TOKEN"))
	if accessKey == "" || secretKey == "" {
		return nil, errors.New("private worker artifact credentials are not configured")
	}
	client, err := tos.NewClientV2(endpoint,
		tos.WithRegion(region),
		tos.WithCredentialsProvider(tos.NewStaticCredentialsProvider(accessKey, secretKey, securityToken)),
		tos.WithConnectionTimeout(15*time.Second),
		tos.WithRequestTimeout(15*time.Minute),
		tos.WithMaxRetryCount(4),
	)
	if err != nil {
		return nil, fmt.Errorf("create TOS client: %w", err)
	}
	return client, nil
}
