package blobs

// Live test against a real S3-compatible endpoint. Set KERN_TEST_S3_ENDPOINT
// (+ BUCKET, ACCESS_KEY, SECRET_KEY) to run; skips otherwise.

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestS3LiveRoundTrip(t *testing.T) {
	endpoint := os.Getenv("KERN_TEST_S3_ENDPOINT")
	bucket := os.Getenv("KERN_TEST_S3_BUCKET")
	if endpoint == "" || bucket == "" {
		t.Skip("KERN_TEST_S3_ENDPOINT/BUCKET not set")
	}
	// Create the bucket if the endpoint allows it; existing is fine.
	if mk, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(os.Getenv("KERN_TEST_S3_ACCESS_KEY"),
			os.Getenv("KERN_TEST_S3_SECRET_KEY"), ""),
		Secure: os.Getenv("KERN_TEST_S3_SECURE") == "true", BucketLookup: minio.BucketLookupPath,
	}); err == nil {
		_ = mk.MakeBucket(context.Background(), bucket, minio.MakeBucketOptions{})
	}
	st, err := NewS3(S3Config{
		Endpoint:  endpoint,
		Region:    os.Getenv("KERN_TEST_S3_REGION"),
		Bucket:    bucket,
		Prefix:    "kern-live-test",
		AccessKey: os.Getenv("KERN_TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("KERN_TEST_S3_SECRET_KEY"),
		Secure:    os.Getenv("KERN_TEST_S3_SECURE") == "true",
		PathStyle: true,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
	key := "logs/live/t.log"

	n, err := st.Append(key, []byte("hello "))
	if err != nil || n != 6 {
		t.Fatalf("append1: %d %v", n, err)
	}
	n, err = st.Append(key, []byte("world"))
	if err != nil || n != 11 {
		t.Fatalf("append2: %d %v", n, err)
	}
	rc, size, err := st.Open(key)
	if err != nil || size != 11 {
		t.Fatalf("open: %d %v", size, err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()
	if string(body) != "hello world" {
		t.Fatalf("body %q", body)
	}
	part, total, err := st.ReadFrom(key, 6, 5)
	if err != nil || string(part) != "world" || total != 11 {
		t.Fatalf("range: %q %d %v", part, total, err)
	}
	if _, sum, err := st.Put("art/x.bin", bytes.NewReader([]byte("artifact"))); err != nil || sum == "" {
		t.Fatalf("put: %v", err)
	}
	if size, _ := st.Size("art/x.bin"); size != 8 {
		t.Fatalf("size %d", size)
	}
	if err := st.Delete("art/x.bin"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if size, _ := st.Size("art/x.bin"); size != 0 {
		t.Fatalf("size after delete %d", size)
	}
	if err := st.Delete(key); err != nil {
		t.Fatalf("delete log: %v", err)
	}
}
