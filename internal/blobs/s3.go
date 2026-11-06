package blobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Config selects the S3/MinIO blob driver. Set Bucket to enable it.
type S3Config struct {
	Endpoint  string // host[:port]; empty = AWS S3 for Region
	Region    string
	Bucket    string
	Prefix    string // optional key prefix, e.g. "kern"
	AccessKey string
	SecretKey string
	Secure    bool // https unless false
	PathStyle bool // MinIO and most S3-compatible services need path-style
}

// S3 stores blobs in an S3-compatible bucket. It satisfies Store but has one
// structural cost: Append is read-modify-write, because S3 objects are
// immutable and multipart parts are invisible until the upload completes —
// live log streaming needs a readable object after every flush.
// shortcut: full-object rewrite per append, bounded by s3MaxAppendObject;
// upgrade to a parts-manifest scheme if individual logs regularly exceed it.
type S3 struct {
	client *minio.Client
	bucket string
	prefix string
	locks  sync.Map // key -> *sync.Mutex, serializes Append per key
}

func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3 blob driver requires a bucket")
	}
	var creds *credentials.Credentials
	if cfg.AccessKey != "" {
		creds = credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	} else {
		creds = credentials.NewChainCredentials([]credentials.Provider{
			&credentials.EnvAWS{},
			&credentials.EnvMinio{},
			&credentials.IAM{},
		})
	}
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	cl, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:        creds,
		Secure:       cfg.Secure,
		Region:       cfg.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	return &S3{client: cl, bucket: cfg.Bucket, prefix: strings.Trim(cfg.Prefix, "/")}, nil
}

// key validates and prefixes a blob key. The guard matches Filesystem.path.
func (s *S3) key(k string) (string, error) {
	clean := path.Clean(strings.TrimPrefix(k, "/"))
	if clean == "." || strings.HasPrefix(clean, "..") || path.IsAbs(clean) {
		return "", fmt.Errorf("invalid blob key %q", k)
	}
	if s.prefix != "" {
		return s.prefix + "/" + clean, nil
	}
	return clean, nil
}

func (s *S3) lockFor(k string) *sync.Mutex {
	l, _ := s.locks.LoadOrStore(k, &sync.Mutex{})
	return l.(*sync.Mutex)
}

func isNoSuchKey(err error) bool {
	return minio.ToErrorResponse(err).Code == "NoSuchKey"
}

// s3MaxAppendObject bounds the read-modify-write Append path: beyond it the
// driver refuses rather than buffering an unbounded object in api memory.
// 32 MiB covers CI logs comfortably; anything larger needs the parts-manifest
// upgrade noted on S3.
const s3MaxAppendObject = 32 << 20

func (s *S3) Append(key string, data []byte) (int64, error) {
	k, err := s.key(key)
	if err != nil {
		return 0, err
	}
	lock := s.lockFor(k)
	lock.Lock()
	defer lock.Unlock()
	ctx := context.Background()
	var body []byte
	obj, err := s.client.GetObject(ctx, s.bucket, k, minio.GetObjectOptions{})
	if err != nil {
		return 0, err
	}
	body, err = io.ReadAll(io.LimitReader(obj, s3MaxAppendObject+1))
	obj.Close()
	if err != nil {
		if !isNoSuchKey(err) {
			return 0, err
		}
		body = nil
	}
	if int64(len(body))+int64(len(data)) > s3MaxAppendObject {
		return 0, fmt.Errorf("blob %q exceeds the %d MiB append ceiling", key, s3MaxAppendObject>>20)
	}
	body = append(body, data...)
	_, err = s.client.PutObject(ctx, s.bucket, k, bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{})
	if err != nil {
		return 0, err
	}
	return int64(len(body)), nil
}

func (s *S3) Put(key string, r io.Reader) (int64, string, error) {
	k, err := s.key(key)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	var buf bytes.Buffer
	n, err := io.Copy(io.MultiWriter(&buf, h), r)
	if err != nil {
		return 0, "", err
	}
	_, err = s.client.PutObject(context.Background(), s.bucket, k, bytes.NewReader(buf.Bytes()), n, minio.PutObjectOptions{})
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

func (s *S3) ReadFrom(key string, offset int64, max int) ([]byte, int64, error) {
	k, err := s.key(key)
	if err != nil {
		return nil, 0, err
	}
	ctx := context.Background()
	info, err := s.client.StatObject(ctx, s.bucket, k, minio.StatObjectOptions{})
	if err != nil {
		if isNoSuchKey(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	size := info.Size
	if offset >= size {
		return nil, size, nil
	}
	if max <= 0 || max > 1<<20 {
		max = 256 << 10
	}
	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(offset, offset+int64(max)-1); err != nil {
		return nil, size, err
	}
	obj, err := s.client.GetObject(ctx, s.bucket, k, opts)
	if err != nil {
		return nil, size, err
	}
	defer obj.Close()
	buf, err := io.ReadAll(obj)
	if err != nil {
		return nil, size, err
	}
	return buf, size, nil
}

func (s *S3) Open(key string) (io.ReadCloser, int64, error) {
	k, err := s.key(key)
	if err != nil {
		return nil, 0, err
	}
	obj, err := s.client.GetObject(context.Background(), s.bucket, k, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, err
	}
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		if isNoSuchKey(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	return obj, info.Size, nil
}

func (s *S3) Size(key string) (int64, error) {
	k, err := s.key(key)
	if err != nil {
		return 0, err
	}
	info, err := s.client.StatObject(context.Background(), s.bucket, k, minio.StatObjectOptions{})
	if err != nil {
		if isNoSuchKey(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size, nil
}

func (s *S3) Delete(key string) error {
	k, err := s.key(key)
	if err != nil {
		return err
	}
	return s.client.RemoveObject(context.Background(), s.bucket, k, minio.RemoveObjectOptions{})
}
