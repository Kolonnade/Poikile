package s3_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Kolonnade/Poikile/assettest"
	"github.com/Kolonnade/Poikile/bucket"
	"github.com/Kolonnade/Poikile/cache"
	"github.com/Kolonnade/Poikile/process"
	pimage "github.com/Kolonnade/Poikile/process/image"
	pt "github.com/Kolonnade/Poikile/process/image/imagetest"
	"github.com/Kolonnade/Poikile/serve"
	"github.com/Kolonnade/Poikile/store"
	"github.com/Kolonnade/Poikile/store/s3"
	"github.com/Kolonnade/Poikile/upload"
)

// mapResolver authorizes every request for the assets it holds.
type mapResolver map[string]serve.Asset

func (m mapResolver) Resolve(_ *http.Request, id, variant string) (serve.Asset, error) {
	a, ok := m[id+"/"+variant]
	if !ok {
		return serve.Asset{}, serve.ErrNotFound
	}
	return a, nil
}

// TestFilesEndToEnd runs a room's whole life against an S3-compatible
// endpoint (TEST_S3_ENDPOINT, as TestConformance): ensure the bucket,
// upload a file and an image through signed POSTs, store the file as
// uploaded, keep the image's bytes and make its variants, serve both,
// delete every key with eviction, then delete the bucket.
func TestFilesEndToEnd(t *testing.T) {
	endpoint := os.Getenv("TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_S3_ENDPOINT not set")
	}
	ctx := context.Background()
	p, err := s3.FromStatic(endpoint, os.Getenv("TEST_S3_ACCESS_KEY"), os.Getenv("TEST_S3_SECRET_KEY"),
		s3.Options{Region: os.Getenv("TEST_S3_REGION"), AllowUnsupported: true})
	if err != nil {
		t.Fatal(err)
	}
	prefix := os.Getenv("TEST_S3_BUCKET_PREFIX")
	if prefix == "" {
		prefix = "poikile-test-"
	}
	room := uuid(t)
	name, err := bucket.Name(prefix, "rm", room)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("bucket %s", name)
	if err := p.EnsureBucket(ctx, name, bucket.Baseline("http://localhost:8080")); err != nil {
		t.Fatal(err)
	}
	var keys []string
	t.Cleanup(func() {
		for _, k := range keys {
			p.Delete(ctx, name, k)
		}
		if err := p.DeleteBucket(ctx, name); err != nil {
			t.Errorf("cleanup: %v (remove %s by hand)", err, name)
		}
	})

	sendFile := func(ct string, body []byte) string {
		t.Helper()
		id := uuid(t)
		k, _ := store.IncomingKey(id)
		keys = append(keys, k)
		tg, err := p.SignUpload(ctx, name, k, upload.Limits{ContentType: ct, MaxBytes: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		if st, err := assettest.Send(ctx, tg, ct, body); err != nil || st/100 != 2 {
			t.Fatalf("upload %s: %d, %v", ct, st, err)
		}
		return id
	}

	// A log file.
	logBytes := []byte("step 1 ok\nstep 2 <script>alert(1)</script>\n")
	logUp, logID := sendFile("text/plain", logBytes), uuid(t)
	lf, err := process.StoreFile(ctx, p, name, logUp, logID, process.FileOptions{MaxBytes: 1 << 20, Size: int64(len(logBytes))})
	if err != nil {
		t.Fatal(err)
	}
	keys = append(keys, lf.Key)
	if lf.DeclaredType != "text/plain" || lf.SniffedType != "text/plain; charset=utf-8" {
		t.Errorf("log types = %q, %q", lf.DeclaredType, lf.SniffedType)
	}

	// A photo: keep its bytes as a file, and make its variants.
	jpeg := pt.JPEG(640, 480, nil, &pt.EXIF{GPS: true})
	imgUp, imgID := sendFile("image/jpeg", jpeg), uuid(t)
	jf, err := process.StoreFile(ctx, p, name, imgUp, imgID, process.FileOptions{MaxBytes: 1 << 20, KeepIncoming: true})
	if err != nil {
		t.Fatal(err)
	}
	keys = append(keys, jf.Key)
	pl, err := process.New("", pimage.DefaultConfig(), 1)
	if err != nil {
		t.Fatal(err)
	}
	res, err := pl.Run(ctx, p, name, imgUp, imgID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range res.Variants {
		keys = append(keys, v.Key)
	}

	// A declared image that isn't one is refused.
	badUp := sendFile("image/png", []byte("%PDF-1.7 not a png"))
	if _, err := process.StoreFile(ctx, p, name, badUp, uuid(t), process.FileOptions{MaxBytes: 1 << 20}); !errors.Is(err, process.ErrFileType) {
		t.Errorf("fake png: %v", err)
	}

	now := time.Now()
	res0 := res.Variants[0]
	resolver := mapResolver{
		logID + "/file":         {ID: logID, Bucket: name, Key: lf.Key, ContentType: lf.DeclaredType, Size: lf.Size, CreatedAt: now, ETag: "l", Attachment: true, Filename: "build log.txt"},
		imgID + "/file":         {ID: imgID, Bucket: name, Key: jf.Key, ContentType: jf.DeclaredType, Size: jf.Size, CreatedAt: now, ETag: "f", Attachment: true, Filename: "IMG_0001.jpg"},
		imgID + "/" + res0.Name: {ID: imgID, Bucket: name, Key: res0.Key, ContentType: res0.ContentType, Size: res0.Size, CreatedAt: now, ETag: "t"},
	}
	c := cache.New(cache.Config{Tiers: []cache.Tier{{Name: "hot", Bytes: 1 << 24}}})
	h := serve.New(serve.Config{Resolver: resolver, Provider: p, Cache: c, Variants: []string{res0.Name, process.FileVariant}, StreamAbove: 1 << 10})
	get := func(id, v string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/"+id+"?size="+v, nil))
		return rec
	}
	for _, tc := range []struct {
		id, v, disp string
		body        []byte
	}{
		{logID, "file", `attachment; filename="build log.txt"`, logBytes},
		{imgID, "file", `attachment; filename="IMG_0001.jpg"`, jpeg},
		{imgID, res0.Name, "inline", nil},
	} {
		rec := get(tc.id, tc.v)
		hd := rec.Header()
		if rec.Code != 200 || hd.Get("Content-Disposition") != tc.disp || hd.Get("Content-Security-Policy") != "sandbox" || hd.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s?size=%s: %d %v", tc.id, tc.v, rec.Code, hd)
		}
		if tc.body != nil && rec.Body.String() != string(tc.body) {
			t.Errorf("%s?size=%s: body differs", tc.id, tc.v)
		}
	}

	// Delete the room: every key, with eviction, then the bucket.
	if err := p.DeleteBucket(ctx, name); !errors.Is(err, store.ErrBucketNotEmpty) {
		t.Fatalf("DeleteBucket before emptying: %v", err)
	}
	for _, k := range keys {
		if err := serve.Delete(ctx, c, p, name, k); err != nil {
			t.Fatalf("delete %s: %v", k, err)
		}
	}
	if rec := get(logID, "file"); rec.Code != 404 {
		t.Errorf("log served after delete: %d", rec.Code)
	}
	for i := 0; i < 2; i++ {
		if err := p.DeleteBucket(ctx, name); err != nil {
			t.Fatalf("DeleteBucket #%d: %v", i+1, err)
		}
	}
}

func uuid(t *testing.T) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
