// Package storage stores processed photos in a private Backblaze B2 bucket through its
// S3-compatible API and hands out short-lived signed URLs for viewing them.
package storage

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"meowshot/internal/config"
)

// Storage is the object store used by the API. Keys are never exposed as public URLs.
type Storage interface {
	Put(ctx context.Context, key string, body []byte, contentType string) error
	Delete(ctx context.Context, keys ...string) error
	SignedURL(ctx context.Context, key string) (string, error)
}

type B2 struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	ttl     time.Duration
}

func NewB2(cfg config.StorageConfig, ttl time.Duration) *B2 {
	client := s3.New(s3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.KeyID, cfg.AppKey, ""),
		UsePathStyle: cfg.UsePathStyle,
		// B2 rejects some of the newer default checksum headers; only send them when an API requires it.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &B2{client: client, presign: s3.NewPresignClient(client), bucket: cfg.Bucket, ttl: ttl}
}

func (b *B2) Put(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(b.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(body),
		ContentLength: aws.Int64(int64(len(body))),
		ContentType:   aws.String(contentType),
		CacheControl:  aws.String("private, max-age=86400, immutable"),
	})
	if err != nil {
		return fmt.Errorf("storage put %s: %w", key, err)
	}
	return nil
}

// Delete removes objects one by one (B2's DeleteObjects needs Content-MD5, single deletes do not).
func (b *B2) Delete(ctx context.Context, keys ...string) error {
	var firstErr error
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(k)}); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("storage delete %s: %w", k, err)
		}
	}
	return firstErr
}

func (b *B2) SignedURL(ctx context.Context, key string) (string, error) {
	req, err := b.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)},
		s3.WithPresignExpires(b.ttl))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}
