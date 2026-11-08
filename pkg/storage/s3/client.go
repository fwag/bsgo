package s3

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Storage struct {
	client *awss3.Client
	bucket string
}

func NewS3Storage(ctx context.Context, endpoint, region, bucket, accessKey, secretKey string) (*S3Storage, error) {
	if region == "" {
		region = "garage"
	}
	if accessKey == "" {
		accessKey = "GKbf3351404239790955497192"
		secretKey = "4b0c49dccb91c93a3991ddb7f0a1198546343c2dd967b55897f44ba24a33eefb"
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load S3 config: %w", err)
	}

	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		if endpoint != "" {
			normalizedEndpoint := strings.Replace(endpoint, "://localhost:", "://127.0.0.1:", 1)
			o.BaseEndpoint = aws.String(normalizedEndpoint)
			o.UsePathStyle = true
		}
	})

	return &S3Storage{
		client: client,
		bucket: bucket,
	}, nil
}

func (s *S3Storage) UploadFile(ctx context.Context, localPath, s3Key string) error {
	cleanPath := filepath.Clean(localPath)
	file, err := os.Open(cleanPath)
	if err != nil {
		return fmt.Errorf("failed to open local file %s: %w", localPath, err)
	}
	defer file.Close()

	_, err = s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s3Key),
		Body:   file,
	})
	if err != nil {
		return fmt.Errorf("failed to upload to s3://%s/%s: %w", s.bucket, s3Key, err)
	}
	return nil
}

func (s *S3Storage) DownloadFile(ctx context.Context, s3Key, localPath string) error {
	resp, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s3Key),
	})
	if err != nil {
		return fmt.Errorf("failed to get s3://%s/%s: %w", s.bucket, s3Key, err)
	}
	defer resp.Body.Close()

	cleanPath := filepath.Clean(localPath)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0750); err != nil {
		return err
	}

	outFile, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	return err
}

func (s *S3Storage) GetObject(ctx context.Context, s3Key string) (io.ReadCloser, int64, string, error) {
	resp, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s3Key),
	})
	if err != nil {
		return nil, 0, "", fmt.Errorf("failed to get s3://%s/%s: %w", s.bucket, s3Key, err)
	}

	var size int64
	if resp.ContentLength != nil {
		size = *resp.ContentLength
	}
	var contentType string
	if resp.ContentType != nil {
		contentType = *resp.ContentType
	}

	return resp.Body, size, contentType, nil
}

func (s *S3Storage) GetRepoFile(ctx context.Context, s3Key string) (io.ReadCloser, int64, string, error) {
	return s.GetObject(ctx, s3Key)
}

func (s *S3Storage) DownloadPrefix(ctx context.Context, s3Prefix, localDir string) (int, error) {
	paginator := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s3Prefix),
	})

	count := 0
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return count, fmt.Errorf("failed to list s3 objects: %w", err)
		}

		for _, obj := range page.Contents {
			relPath := *obj.Key
			if len(relPath) >= len(s3Prefix) {
				relPath = relPath[len(s3Prefix):]
			}
			localDest := filepath.Join(localDir, relPath)
			if err := s.DownloadFile(ctx, *obj.Key, localDest); err == nil {
				count++
			}
		}
	}
	return count, nil
}
