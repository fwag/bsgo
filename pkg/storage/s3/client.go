package s3

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	awss3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3Storage struct {
	client *awss3.Client
	bucket string
}

func NewS3Storage(ctx context.Context, endpoint, region, bucket, accessKey, secretKey string) (*S3Storage, error) {
	if region == "" {
		region = "garage"
	}
	accessKey, secretKey = resolveCredentials(accessKey, secretKey)
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("missing S3 credentials (set AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY or config/s3-credentials.env)")
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

	tmpPath := fmt.Sprintf("%s.tmp.%d", cleanPath, time.Now().UnixNano())
	outFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(outFile, resp.Body)
	closeErr := outFile.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}

	if err := os.Rename(tmpPath, cleanPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func (s *S3Storage) Bucket() string {
	return s.bucket
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

func (s *S3Storage) DownloadMetadata(ctx context.Context, prefix, localDir string) error {
	cleanPrefix := strings.Trim(filepath.Clean(prefix), "/") + "/"
	_, err := s.DownloadPrefix(ctx, cleanPrefix, localDir)
	return err
}

type repomdManifest struct {
	Data []struct {
		Type     string `xml:"type,attr"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
	} `xml:"data"`
}

func (s *S3Storage) listRPMObjects(ctx context.Context, s3Prefix string) ([]awss3types.Object, error) {
	paginator := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s3Prefix),
	})

	var rpms []awss3types.Object
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list s3 objects: %w", err)
		}
		for _, obj := range page.Contents {
			relPath := *obj.Key
			if len(relPath) >= len(s3Prefix) {
				relPath = relPath[len(s3Prefix):]
			}
			relClean := filepath.Clean(relPath)
			if !strings.HasPrefix(relClean, "repodata/") && relClean != "repodata" {
				rpms = append(rpms, obj)
			}
		}
	}
	return rpms, nil
}

func (s *S3Storage) downloadRPMsWithCache(ctx context.Context, s3Prefix, localDir string, rpms []awss3types.Object) int {
	count := 0
	for _, obj := range rpms {
		relPath := (*obj.Key)[len(s3Prefix):]
		dest := filepath.Join(localDir, relPath)
		if isCachedFile(dest, obj.Size) {
			count++
			continue
		}
		if err := s.DownloadFile(ctx, *obj.Key, dest); err == nil {
			count++
		}
	}
	return count
}

func isCachedFile(path string, expectedSize *int64) bool {
	if expectedSize == nil || *expectedSize <= 0 {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && fi.Size() == *expectedSize
}

func (s *S3Storage) DownloadPrefix(ctx context.Context, s3Prefix, localDir string) (int, error) {
	rpms, err := s.listRPMObjects(ctx, s3Prefix)
	if err != nil {
		return 0, err
	}

	count := s.downloadRPMsWithCache(ctx, s3Prefix, localDir, rpms)

	metaCount, err := s.downloadRepodataManifest(ctx, s3Prefix, localDir)
	if err != nil {
		return count, err
	}
	count += metaCount

	return count, nil
}

func (s *S3Storage) downloadRepodataManifest(ctx context.Context, s3Prefix, localDir string) (int, error) {
	stagingDir := filepath.Join(localDir, fmt.Sprintf(".repodata.stage.%d", time.Now().UnixNano()))
	if err := os.MkdirAll(stagingDir, 0750); err != nil {
		return 0, err
	}
	defer func() {
		_ = os.RemoveAll(stagingDir)
	}()

	repomdKey := s3Prefix + "repodata/repomd.xml"
	localRepomd := filepath.Join(stagingDir, "repomd.xml")
	if err := s.DownloadFile(ctx, repomdKey, localRepomd); err != nil {
		return 0, nil // No repodata published yet
	}

	_ = s.DownloadFile(ctx, s3Prefix+"repodata/repomd.xml.asc", filepath.Join(stagingDir, "repomd.xml.asc"))
	_ = s.DownloadFile(ctx, s3Prefix+"repodata/repomd.xml.key", filepath.Join(stagingDir, "repomd.xml.key"))

	repomdBytes, err := os.ReadFile(localRepomd)
	if err != nil {
		return 0, err
	}

	var manifest repomdManifest
	if err := xml.Unmarshal(repomdBytes, &manifest); err != nil {
		return 0, fmt.Errorf("failed to parse repomd.xml: %w", err)
	}

	count := 1
	for _, entry := range manifest.Data {
		href := entry.Location.Href
		if href == "" {
			continue
		}
		dest := filepath.Join(stagingDir, filepath.Base(href))
		s3Key := s3Prefix + strings.TrimPrefix(href, "/")
		if err := s.downloadWithRetry(ctx, s3Key, dest, 3); err != nil {
			return 0, fmt.Errorf("failed to download manifest file %s: %w", s3Key, err)
		}
		count++
	}

	finalDir := filepath.Join(localDir, "repodata")
	if err := atomicSwapDir(stagingDir, finalDir); err != nil {
		return 0, fmt.Errorf("failed to atomically replace repodata: %w", err)
	}

	return count, nil
}

func (s *S3Storage) downloadWithRetry(ctx context.Context, s3Key, dest string, retries int) error {
	var err error
	for i := 0; i < retries; i++ {
		err = s.DownloadFile(ctx, s3Key, dest)
		if err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

func atomicSwapDir(stagingDir, finalDir string) error {
	trashDir := fmt.Sprintf("%s.trash.%d", finalDir, time.Now().UnixNano())
	if _, err := os.Stat(finalDir); err == nil {
		if err := os.Rename(finalDir, trashDir); err != nil {
			return err
		}
	}
	if err := os.Rename(stagingDir, finalDir); err != nil {
		_ = os.Rename(trashDir, finalDir)
		return err
	}
	_ = os.RemoveAll(trashDir)
	return nil
}

// S3ObjectInfo represents metadata for an object in an S3 bucket.
type S3ObjectInfo struct {
	Key          string
	LastModified time.Time
	Size         int64
}

// ListObjects retrieves all objects matching the given prefix.
func (s *S3Storage) ListObjects(ctx context.Context, prefix string) ([]S3ObjectInfo, error) {
	paginator := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	var res []S3ObjectInfo
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed listing objects for prefix %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			var mod time.Time
			if obj.LastModified != nil {
				mod = *obj.LastModified
			}
			var size int64
			if obj.Size != nil {
				size = *obj.Size
			}
			res = append(res, S3ObjectInfo{
				Key:          aws.ToString(obj.Key),
				LastModified: mod,
				Size:         size,
			})
		}
	}
	return res, nil
}

// ReadObject returns the content of an object.
func (s *S3Storage) ReadObject(ctx context.Context, s3Key string) ([]byte, error) {
	rc, _, _, err := s.GetObject(ctx, s3Key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("failed reading s3://%s/%s: %w", s.bucket, s3Key, err)
	}
	return data, nil
}

// CopyObject copies an object inside the bucket without moving its data
// through the caller.
func (s *S3Storage) CopyObject(ctx context.Context, srcKey, dstKey string) error {
	_, err := s.client.CopyObject(ctx, &awss3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		Key:        aws.String(dstKey),
		CopySource: aws.String(url.PathEscape(s.bucket + "/" + srcKey)),
	})
	if err != nil {
		return fmt.Errorf("failed copying s3://%s/%s to %s: %w", s.bucket, srcKey, dstKey, err)
	}
	return nil
}

// DeleteObject deletes an object from S3 by its key.
func (s *S3Storage) DeleteObject(ctx context.Context, s3Key string) error {
	_, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s3Key),
	})
	if err != nil {
		return fmt.Errorf("failed deleting object s3://%s/%s: %w", s.bucket, s3Key, err)
	}
	return nil
}

func resolveCredentials(accessKey, secretKey string) (string, string) {
	if accessKey != "" && secretKey != "" {
		return accessKey, secretKey
	}

	if accessKey == "" {
		accessKey = os.Getenv("AWS_ACCESS_KEY_ID")
		if accessKey == "" {
			accessKey = os.Getenv("S3_ACCESS_KEY")
		}
	}
	if secretKey == "" {
		secretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
		if secretKey == "" {
			secretKey = os.Getenv("S3_SECRET_KEY")
		}
	}
	if accessKey != "" && secretKey != "" {
		return accessKey, secretKey
	}

	envMap := loadCredentialsFile("config/s3-credentials.env")
	if accessKey == "" {
		accessKey = envMap["AWS_ACCESS_KEY_ID"]
		if accessKey == "" {
			accessKey = envMap["S3_ACCESS_KEY"]
		}
	}
	if secretKey == "" {
		secretKey = envMap["AWS_SECRET_ACCESS_KEY"]
		if secretKey == "" {
			secretKey = envMap["S3_SECRET_KEY"]
		}
	}

	return accessKey, secretKey
}

func loadCredentialsFile(path string) map[string]string {
	res := make(map[string]string)
	cleanPath := filepath.Clean(path)
	// #nosec G304 -- loading optional credentials from sanitized path
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return res
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
			res[k] = v
		}
	}
	return res
}
