package r2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObjectKeyAndPublicURL(t *testing.T) {
	if key, ok := ObjectKey("news-assets", "assets/ab/hash.png"); !ok || key != "news-assets/ab/hash.png" {
		t.Fatalf("ObjectKey() = %q, %v", key, ok)
	}
	for _, localPath := range []string{"", "raw/ab/page.html", "assets/../raw/page.html", "assets/../../outside"} {
		if key, ok := ObjectKey("news-assets", localPath); ok {
			t.Fatalf("ObjectKey(%q) unexpectedly returned %q", localPath, key)
		}
	}
	want := "https://oss.swjtu.zip/news-assets/ab/hash.png"
	if got := PublicURL("https://oss.swjtu.zip/news-assets", "assets/ab/hash.png"); got != want {
		t.Fatalf("PublicURL() = %q, want %q", got, want)
	}
}

func TestUploadSetsR2Metadata(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "assets", "ab", "hash.png")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("image-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotPath string
	var gotType, gotClass, gotDisposition string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotType = r.Header.Get("Content-Type")
		gotClass = r.Header.Get("x-amz-storage-class")
		gotDisposition = r.Header.Get("Content-Disposition")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Bucket: "swjtu-zip", Prefix: "news-assets", AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Upload(context.Background(), filePath, "assets/ab/hash.png", "image/png", "通知.png", "attachment", "STANDARD_IA"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/swjtu-zip/news-assets/ab/hash.png" {
		t.Fatalf("unexpected request path %q", gotPath)
	}
	if gotType != "image/png" || gotClass != "STANDARD_IA" || !strings.Contains(gotDisposition, "attachment") {
		t.Fatalf("metadata = type %q class %q disposition %q", gotType, gotClass, gotDisposition)
	}
}

func TestChangeObjectStorageClassUsesServerSideCopy(t *testing.T) {
	var gotPath, gotSource, gotDirective, gotClass string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSource = r.Header.Get("x-amz-copy-source")
		gotDirective = r.Header.Get("x-amz-metadata-directive")
		gotClass = r.Header.Get("x-amz-storage-class")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Bucket: "swjtu-zip", Prefix: "news-assets", AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ChangeObjectStorageClass(context.Background(), "news-assets/ab/hash.png", "STANDARD"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/swjtu-zip/news-assets/ab/hash.png" || gotSource != "/swjtu-zip/news-assets/ab/hash.png" || gotDirective != "COPY" || gotClass != "STANDARD" {
		t.Fatalf("copy request = path %q source %q directive %q class %q", gotPath, gotSource, gotDirective, gotClass)
	}
}

func TestListObjectsIncludesStorageClass(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>news-assets/ia.bin</Key><StorageClass>STANDARD_IA</StorageClass></Contents><Contents><Key>news-assets/standard.bin</Key><StorageClass>STANDARD</StorageClass></Contents></ListBucketResult>`))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Bucket: "swjtu-zip", Prefix: "news-assets", AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := client.ListObjects(context.Background(), "news-assets/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || objects[0].Key != "news-assets/ia.bin" || objects[0].StorageClass != "STANDARD_IA" || objects[1].StorageClass != "STANDARD" {
		t.Fatalf("objects = %#v", objects)
	}
}
