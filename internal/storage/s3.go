package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type S3Driver struct {
	client *s3.Client
	bucket string
	ctx    context.Context
}

func NewS3(ctx context.Context, bucket string) (*S3Driver, error) {
	if bucket == "" {
		return nil, fmt.Errorf("bucket name is required")
	}

	// Load AWS configuration from environment
	// This automatically reads AWS_REGION, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_ENDPOINT_URL
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true // Required for MinIO and other S3-compatible services
	})

	// Verify bucket access
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		return nil, fmt.Errorf("access bucket %s: %w", bucket, err)
	}

	return &S3Driver{
		client: client,
		bucket: bucket,
		ctx:    ctx,
	}, nil
}

func (s *S3Driver) objectKey(cacheFileName string) string {
	return BaseFolderName + "/" + cacheFileName
}

func (s *S3Driver) partObjectKey(uploadID string, partNumber int) string {
	return fmt.Sprintf("%s/%s/%s/part_%d", BaseFolderName, UploadFolderName, uploadID, partNumber)
}

func (s *S3Driver) Delete(cacheFileNames []string) error {
	if len(cacheFileNames) == 0 {
		return nil
	}

	// S3 DeleteObjects supports up to 1000 objects per request
	for i := 0; i < len(cacheFileNames); i += 1000 {
		end := i + 1000
		if end > len(cacheFileNames) {
			end = len(cacheFileNames)
		}
		batch := cacheFileNames[i:end]

		var objects []types.ObjectIdentifier
		for _, name := range batch {
			objects = append(objects, types.ObjectIdentifier{
				Key: aws.String(s.objectKey(name)),
			})
		}

		_, err := s.client.DeleteObjects(s.ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{
				Objects: objects,
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			return fmt.Errorf("delete objects: %w", err)
		}
	}

	return nil
}

func (s *S3Driver) CreateReadStream(cacheFileName string) (io.ReadCloser, error) {
	result, err := s.client.GetObject(s.ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(cacheFileName)),
	})
	if err != nil {
		var notFound *types.NoSuchKey
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("get object: %w", err)
	}
	return result.Body, nil
}

func (s *S3Driver) CreateDownloadURL(cacheFileName string) (string, error) {
	// S3 signed URLs require additional configuration
	// For now, return empty string to use streaming
	return "", nil
}

func (s *S3Driver) UploadPart(uploadID string, partNumber int, data io.Reader) error {
	uploader := manager.NewUploader(s.client)
	_, err := uploader.Upload(s.ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.partObjectKey(uploadID, partNumber)),
		Body:   data,
	})
	if err != nil {
		return fmt.Errorf("upload part %d: %w", partNumber, err)
	}
	return nil
}

func (s *S3Driver) CompleteMultipartUpload(cacheFileName, uploadID string, partNumbers []int) error {
	if len(partNumbers) == 0 {
		return nil
	}

	sort.Ints(partNumbers)

	// Stream parts through a pipe to avoid buffering everything in memory
	pr, pw := io.Pipe()

	// Download parts in a goroutine, writing to the pipe
	go func() {
		var writeErr error
		for _, pn := range partNumbers {
			result, err := s.client.GetObject(s.ctx, &s3.GetObjectInput{
				Bucket: aws.String(s.bucket),
				Key:    aws.String(s.partObjectKey(uploadID, pn)),
			})
			if err != nil {
				pw.CloseWithError(fmt.Errorf("get part %d: %w", pn, err))
				return
			}
			_, writeErr = io.Copy(pw, result.Body)
			result.Body.Close()
			if writeErr != nil {
				pw.CloseWithError(fmt.Errorf("read part %d: %w", pn, writeErr))
				return
			}
		}
		pw.Close()
	}()

	// Upload the streamed data as the final object
	uploader := manager.NewUploader(s.client)
	_, err := uploader.Upload(s.ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.objectKey(cacheFileName)),
		Body:   pr,
	})
	if err != nil {
		return fmt.Errorf("upload combined file: %w", err)
	}

	// Cleanup parts
	return s.CleanupMultipartUpload(uploadID)
}

func (s *S3Driver) CleanupMultipartUpload(uploadID string) error {
	prefix := fmt.Sprintf("%s/%s/%s/", BaseFolderName, UploadFolderName, uploadID)

	// List all objects with the prefix
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})

	var objectsToDelete []types.ObjectIdentifier
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(s.ctx)
		if err != nil {
			return fmt.Errorf("list objects: %w", err)
		}

		for _, obj := range page.Contents {
			objectsToDelete = append(objectsToDelete, types.ObjectIdentifier{
				Key: obj.Key,
			})
		}
	}

	// Delete objects in batches of 1000
	for i := 0; i < len(objectsToDelete); i += 1000 {
		end := i + 1000
		if end > len(objectsToDelete) {
			end = len(objectsToDelete)
		}
		batch := objectsToDelete[i:end]

		if len(batch) == 0 {
			continue
		}

		_, err := s.client.DeleteObjects(s.ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{
				Objects: batch,
				Quiet:   aws.Bool(true),
			},
		})
		if err != nil {
			return fmt.Errorf("delete objects: %w", err)
		}
	}

	return nil
}
