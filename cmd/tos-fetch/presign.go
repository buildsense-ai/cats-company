package main

import (
	"errors"
	"strings"

	"github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/ve-tos-golang-sdk/v2/tos/enum"
)

// Only the selected object's GET is delegated; credentials stay on the server.
func presignObject(endpoint, region, bucket, key string) (string, error) {
	if strings.TrimSpace(endpoint) == "" || strings.TrimSpace(region) == "" ||
		strings.TrimSpace(bucket) == "" || strings.TrimSpace(key) == "" {
		return "", errors.New("endpoint, region, bucket and key are required")
	}
	if strings.ContainsAny(key, "\r\n") {
		return "", errors.New("object key contains a newline")
	}
	client, err := newClient(endpoint, region)
	if err != nil {
		return "", err
	}
	result, err := client.PreSignedURL(&tos.PreSignedURLInput{
		HTTPMethod: enum.HttpMethodGet,
		Bucket:     bucket,
		Key:        key,
		Expires:    600,
	})
	if err != nil {
		return "", errors.New("could not sign private object GET")
	}
	return result.SignedUrl, nil
}
