package s3

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/openSUSE/bsgo/pkg/gc"
)

// maxDeleteBatch is the S3 limit of keys per DeleteObjects call.
const maxDeleteBatch = 1000

// DeleteObjects deletes up to 1000 objects in one request. Keys that do not
// exist are not an error; any other per-key failure is reported.
func (s *S3Storage) DeleteObjects(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if len(keys) > maxDeleteBatch {
		return fmt.Errorf("cannot delete %d objects at once, the limit is %d", len(keys), maxDeleteBatch)
	}
	ids := make([]awss3types.ObjectIdentifier, len(keys))
	for i, k := range keys {
		ids[i] = awss3types.ObjectIdentifier{Key: aws.String(k)}
	}
	out, err := s.client.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
		Bucket: aws.String(s.bucket),
		Delete: &awss3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
	})
	if err != nil {
		return fmt.Errorf("failed deleting %d objects from s3://%s: %w", len(keys), s.bucket, err)
	}
	if n := len(out.Errors); n > 0 {
		first := out.Errors[0]
		return fmt.Errorf("failed deleting %d of %d objects from s3://%s: %s: %s (%s)",
			n, len(keys), s.bucket, aws.ToString(first.Key), aws.ToString(first.Message), aws.ToString(first.Code))
	}
	return nil
}

// GCObjects adapts an S3Storage to gc.Objects.
type GCObjects struct{ Store *S3Storage }

var _ gc.Objects = GCObjects{}

// List implements gc.Objects.
func (g GCObjects) List(ctx context.Context, prefix string) ([]gc.Object, error) {
	objs, err := g.Store.ListObjects(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]gc.Object, len(objs))
	for i, o := range objs {
		out[i] = gc.Object{Key: o.Key, LastModified: o.LastModified, Size: o.Size}
	}
	return out, nil
}

// Get implements gc.Objects.
func (g GCObjects) Get(ctx context.Context, key string) ([]byte, error) {
	return g.Store.ReadObject(ctx, key)
}

// DeleteBatch implements gc.Objects.
func (g GCObjects) DeleteBatch(ctx context.Context, keys []string) error {
	return g.Store.DeleteObjects(ctx, keys)
}
