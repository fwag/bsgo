package s3

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// PutBytes stores data under s3Key.
func (s *S3Storage) PutBytes(ctx context.Context, s3Key string, data []byte) error {
	_, err := s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s3Key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("failed to upload to s3://%s/%s: %w", s.bucket, s3Key, err)
	}
	return nil
}

// Blobs adapts an S3Storage to the byte-slice object interface of the build
// repository index (buildrepo.Blobs) and adds key listing.
type Blobs struct{ Store *S3Storage }

// GetObject returns the content of key.
func (b Blobs) GetObject(ctx context.Context, key string) ([]byte, error) {
	return b.Store.ReadObject(ctx, key)
}

// PutObject stores data under key.
func (b Blobs) PutObject(ctx context.Context, key string, data []byte) error {
	return b.Store.PutBytes(ctx, key, data)
}

// ListKeys returns the keys of all objects below prefix.
func (b Blobs) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	objs, err := b.Store.ListObjects(ctx, prefix)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(objs))
	for i, o := range objs {
		keys[i] = o.Key
	}
	return keys, nil
}
