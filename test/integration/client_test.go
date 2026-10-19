//go:build integration

// Package integration exercises a real s3lab server with aws-sdk-go-v2.
// Owner: task T3.B.
package integration

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	testAccessKey = "test-access"
	testSecretKey = "test-secret"
	testRegion    = "us-east-1"
)

// newClient returns an S3 client pointed at endpoint with path-style addressing.
func newClient(t *testing.T, endpoint string) *s3.Client {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(testRegion),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

func newUploader(c *s3.Client) *manager.Uploader {
	return manager.NewUploader(c, func(u *manager.Uploader) { u.PartSize = 5 << 20 })
}
