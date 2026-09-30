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

func TestDownloadFilename(t *testing.T) {
	cases := []struct {
		filename, localPath, want string
	}{
		{"附件1", "assets/ab/hash.pdf", "附件1.pdf"},
		{"附件1", "assets\\ab\\hash.docx", "附件1.docx"},
		{"通知.pdf", "assets/ab/hash.pdf", "通知.pdf"},
		{"附件1", "assets/ab/hash", "附件1"},
		{"附件1", "", "附件1"},
		{"", "assets/ab/hash.pdf", ""},
		{"  附件2  ", "assets/ab/hash.bin", "附件2.bin"},
	}
	for _, tc := range cases {
		if got := DownloadFilename(tc.filename, tc.localPath); got != tc.want {
			t.Errorf("DownloadFilename(%q, %q) = %q, want %q", tc.filename, tc.localPath, got, tc.want)
		}
	}
}

func TestUploadCompletesMissingFilenameExtension(t *testing.T) {
	var gotDisposition string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDisposition = r.Header.Get("Content-Disposition")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Bucket: "swjtu-zip", Prefix: "news-assets", AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.UploadBytes(context.Background(), []byte("data"), "assets/ab/hash.pdf", "application/pdf", "附件1", "attachment", "STANDARD"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotDisposition, "attachment") || !strings.Contains(gotDisposition, ".pdf") {
		t.Fatalf("disposition %q does not carry the .pdf extension", gotDisposition)
	}
}

func TestUpdateObjectMetadataUsesReplaceCopy(t *testing.T) {
	var gotPath, gotSource, gotDirective, gotClass, gotType, gotDisposition string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSource = r.Header.Get("x-amz-copy-source")
		gotDirective = r.Header.Get("x-amz-metadata-directive")
		gotClass = r.Header.Get("x-amz-storage-class")
		gotType = r.Header.Get("Content-Type")
		gotDisposition = r.Header.Get("Content-Disposition")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Bucket: "swjtu-zip", Prefix: "news-assets", AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	key := "news-assets/ab/hash.pdf"
	if err := client.UpdateObjectMetadata(context.Background(), key, "application/pdf", "附件1.pdf", "STANDARD_IA"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/swjtu-zip/"+key || gotSource != "/swjtu-zip/"+key || gotDirective != "REPLACE" || gotClass != "STANDARD_IA" {
		t.Fatalf("copy request = path %q source %q directive %q class %q", gotPath, gotSource, gotDirective, gotClass)
	}
	if gotType != "application/pdf" || !strings.Contains(gotDisposition, ".pdf") {
		t.Fatalf("metadata = type %q disposition %q", gotType, gotDisposition)
	}
	if err := client.UpdateObjectMetadata(context.Background(), "../escape", "application/pdf", "a.pdf", "STANDARD"); err == nil {
		t.Fatal("expected invalid key error")
	}
}

func TestGetObjectBytesUsesRangeHeader(t *testing.T) {
	var gotPath, gotRange string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotRange = r.Header.Get("Range")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("%PDF-1.7 sniffed"))
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Bucket: "swjtu-zip", Prefix: "news-assets", AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := client.GetObjectBytes(context.Background(), "news-assets/ab/hash.bin", 65536)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/swjtu-zip/news-assets/ab/hash.bin" || gotRange != "bytes=0-65535" {
		t.Fatalf("get request = path %q range %q", gotPath, gotRange)
	}
	if string(data) != "%PDF-1.7 sniffed" {
		t.Fatalf("data = %q", data)
	}
}

func TestCopyObjectReplacesMetadataOnNewKey(t *testing.T) {
	var gotPath, gotSource, gotDirective, gotClass, gotType, gotDisposition string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSource = r.Header.Get("x-amz-copy-source")
		gotDirective = r.Header.Get("x-amz-metadata-directive")
		gotClass = r.Header.Get("x-amz-storage-class")
		gotType = r.Header.Get("Content-Type")
		gotDisposition = r.Header.Get("Content-Disposition")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(Config{Endpoint: server.URL, Bucket: "swjtu-zip", Prefix: "news-assets", AccessKeyID: "key", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	err = client.CopyObject(context.Background(), "news-assets/ab/hash.bin", "news-assets/ab/hash.pdf", "application/pdf", "附件1.pdf", "attachment", "STANDARD_IA")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/swjtu-zip/news-assets/ab/hash.pdf" || gotSource != "/swjtu-zip/news-assets/ab/hash.bin" {
		t.Fatalf("copy = path %q source %q", gotPath, gotSource)
	}
	if gotDirective != "REPLACE" || gotClass != "STANDARD_IA" || gotType != "application/pdf" || !strings.Contains(gotDisposition, ".pdf") {
		t.Fatalf("metadata = directive %q class %q type %q disposition %q", gotDirective, gotClass, gotType, gotDisposition)
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
