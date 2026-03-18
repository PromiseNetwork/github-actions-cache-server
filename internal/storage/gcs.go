package storage

import (
	"context"
	"fmt"
	"io"
	"time"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

type GCSDriver struct {
	bucket *storage.BucketHandle
	ctx    context.Context
}

type GCSOptions struct {
	Bucket            string
	ServiceAccountKey string // path to service account key file (optional)
	Endpoint          string // custom API endpoint (optional)
}

func NewGCS(ctx context.Context, opts GCSOptions) (*GCSDriver, error) {
	var clientOpts []option.ClientOption
	if opts.ServiceAccountKey != "" {
		clientOpts = append(clientOpts, option.WithCredentialsFile(opts.ServiceAccountKey))
	}
	if opts.Endpoint != "" {
		clientOpts = append(clientOpts, option.WithEndpoint(opts.Endpoint))
		// When using a custom endpoint (e.g., fake-gcs-server), skip authentication
		if opts.ServiceAccountKey == "" {
			clientOpts = append(clientOpts, option.WithoutAuthentication())
		}
	}

	client, err := storage.NewClient(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("create gcs client: %w", err)
	}

	bucket := client.Bucket(opts.Bucket)

	// Verify bucket access
	if _, err := bucket.Attrs(ctx); err != nil {
		return nil, fmt.Errorf("access bucket %s: %w", opts.Bucket, err)
	}

	return &GCSDriver{bucket: bucket, ctx: ctx}, nil
}

func (g *GCSDriver) objectName(cacheFileName string) string {
	return BaseFolderName + "/" + cacheFileName
}

func (g *GCSDriver) partObjectName(uploadID string, partNumber int) string {
	return fmt.Sprintf("%s/%s/%s/part_%d", BaseFolderName, UploadFolderName, uploadID, partNumber)
}

func (g *GCSDriver) Delete(cacheFileNames []string) error {
	for _, name := range cacheFileNames {
		obj := g.bucket.Object(g.objectName(name))
		if err := obj.Delete(g.ctx); err != nil && err != storage.ErrObjectNotExist {
			return fmt.Errorf("delete %s: %w", name, err)
		}
	}
	return nil
}

func (g *GCSDriver) CreateReadStream(cacheFileName string) (io.ReadCloser, error) {
	obj := g.bucket.Object(g.objectName(cacheFileName))
	reader, err := obj.NewReader(g.ctx)
	if err == storage.ErrObjectNotExist {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return reader, nil
}

func (g *GCSDriver) CreateDownloadURL(cacheFileName string) (string, error) {
	url, err := g.bucket.SignedURL(g.objectName(cacheFileName), &storage.SignedURLOptions{
		Method:  "GET",
		Expires: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		return "", fmt.Errorf("sign url: %w", err)
	}
	return url, nil
}

func (g *GCSDriver) UploadPart(uploadID string, partNumber int, data io.Reader) error {
	obj := g.bucket.Object(g.partObjectName(uploadID, partNumber))
	w := obj.NewWriter(g.ctx)
	if _, err := io.Copy(w, data); err != nil {
		w.Close()
		return fmt.Errorf("upload part %d: %w", partNumber, err)
	}
	return w.Close()
}

func (g *GCSDriver) CompleteMultipartUpload(cacheFileName, uploadID string, partNumbers []int) error {
	if len(partNumbers) == 0 {
		return nil
	}

	dst := g.bucket.Object(g.objectName(cacheFileName))

	// Use GCS Compose API to combine parts without downloading/re-uploading.
	// Compose supports up to 32 objects per call, so we compose in batches.
	var sources []*storage.ObjectHandle
	for _, pn := range partNumbers {
		sources = append(sources, g.bucket.Object(g.partObjectName(uploadID, pn)))
	}

	// Compose in batches of 32
	for len(sources) > 1 {
		var nextSources []*storage.ObjectHandle
		for i := 0; i < len(sources); i += 32 {
			end := i + 32
			if end > len(sources) {
				end = len(sources)
			}
			batch := sources[i:end]

			if len(batch) == 1 {
				nextSources = append(nextSources, batch[0])
				continue
			}

			// For intermediate composes, use a temp object
			var target *storage.ObjectHandle
			if len(nextSources) == 0 && end == len(sources) {
				// Last batch, compose directly to destination
				target = dst
			} else {
				target = g.bucket.Object(fmt.Sprintf("%s/%s/%s/compose_temp_%d",
					BaseFolderName, UploadFolderName, uploadID, i))
			}

			composer := target.ComposerFrom(batch...)
			if _, err := composer.Run(g.ctx); err != nil {
				return fmt.Errorf("compose batch at %d: %w", i, err)
			}

			if target != dst {
				nextSources = append(nextSources, target)
			}
		}

		if len(nextSources) == 0 {
			// We composed directly to dst in the last iteration
			break
		}
		sources = nextSources
	}

	// If only one source, copy it to destination
	if len(sources) == 1 && sources[0] != dst {
		copier := dst.CopierFrom(sources[0])
		if _, err := copier.Run(g.ctx); err != nil {
			return fmt.Errorf("copy single part to dest: %w", err)
		}
	}

	return g.CleanupMultipartUpload(uploadID)
}

func (g *GCSDriver) CleanupMultipartUpload(uploadID string) error {
	prefix := fmt.Sprintf("%s/%s/%s/", BaseFolderName, UploadFolderName, uploadID)
	it := g.bucket.Objects(g.ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := it.Next()
		if err != nil {
			break // iterator.Done or error
		}
		_ = g.bucket.Object(attrs.Name).Delete(g.ctx)
	}
	return nil
}
