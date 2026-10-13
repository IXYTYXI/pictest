package gallery

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "test-secret-token-with-at-least-32-characters"

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{ListenAddr: ":8080", PublicBaseURL: "https://images.example.test", DataDir: t.TempDir(), UploadToken: testToken, MaxUploadBytes: 1024 * 1024, MaxStorageBytes: 10 * 1024 * 1024, MaxImagePixels: 10000, MaxConcurrentUploads: 2}
}

func newTestServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testImage(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, width, height))
	im.Set(0, 0, color.RGBA{R: 255, A: 255})
	var b bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&b, im)
	case "jpeg":
		err = jpeg.Encode(&b, im, nil)
	case "gif":
		err = gif.Encode(&b, im, nil)
	default:
		t.Fatal("unknown test format")
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func call(s *Server, method, path, contentType, token string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func multipartBody(t *testing.T, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	p, err := w.CreateFormFile("file", "photo.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.Write(data)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, w.FormDataContentType()
}

func upload(t *testing.T, s *Server, data []byte, useJSON bool) *httptest.ResponseRecorder {
	t.Helper()
	if useJSON {
		body, _ := json.Marshal(map[string]string{"filename": "photo.png", "file": base64.StdEncoding.EncodeToString(data)})
		return call(s, "POST", "/api/upload", "application/json", testToken, bytes.NewReader(body))
	}
	body, ct := multipartBody(t, data)
	return call(s, "POST", "/api/upload", ct, testToken, body)
}

func uploadedImage(t *testing.T, w *httptest.ResponseRecorder) Image {
	t.Helper()
	if w.Code != 201 {
		t.Fatalf("upload: status=%d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Success bool  `json:"success"`
		Data    Image `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Success {
		t.Fatal("missing success")
	}
	return body.Data
}

func TestUploadFormatsAndPublicRead(t *testing.T) {
	for _, format := range []string{"png", "jpeg", "gif"} {
		for _, jsonUpload := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%v", format, jsonUpload), func(t *testing.T) {
				s := newTestServer(t, testConfig(t))
				data := testImage(t, format, 3, 2)
				im := uploadedImage(t, upload(t, s, data, jsonUpload))
				if im.Width != 3 || im.Height != 2 || im.Size != int64(len(data)) || im.Filename != "photo.png" || im.ID == "" || im.CreatedAt.IsZero() {
					t.Fatalf("bad metadata: %+v", im)
				}
				if !strings.HasPrefix(im.URL, "https://images.example.test/i/") {
					t.Fatalf("URL=%q", im.URL)
				}
				path := strings.TrimPrefix(im.URL, "https://images.example.test")
				got := call(s, "GET", path, "", "", nil)
				if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), data) || got.Header().Get("Content-Type") != im.MIME {
					t.Fatalf("bad public response %d %v", got.Code, got.Header())
				}
				head := call(s, "HEAD", path, "", "", nil)
				if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Length") != fmt.Sprint(len(data)) {
					t.Fatalf("HEAD: %d %v", head.Code, head.Header())
				}
			})
		}
	}
}

func TestAuthenticationAndOrigin(t *testing.T) {
	s := newTestServer(t, testConfig(t))
	for _, token := range []string{"", "wrong"} {
		for _, path := range []string{"/api/images", "/api/upload", "/api/images/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
			method := "GET"
			if path == "/api/upload" {
				method = "POST"
			}
			if strings.HasPrefix(path, "/api/images/") {
				method = "DELETE"
			}
			w := call(s, method, path, "", token, nil)
			if w.Code != 401 || !strings.Contains(w.Body.String(), `"success":false`) {
				t.Fatalf("%s auth status=%d body=%s", path, w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("private response cached")
			}
		}
	}
	for _, origin := range []string{"https://evil.test", "null"} {
		r := httptest.NewRequest("GET", "/api/images", nil)
		r.Header.Set("Authorization", "Bearer "+testToken)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("cross-site Origin accepted: %s: %d", origin, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/api/images", nil)
	r.Header.Set("Origin", "https://images.example.test")
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("same origin rejected %d", w.Code)
	}
}

func TestPersistenceListAndDelete(t *testing.T) {
	cfg := testConfig(t)
	s := newTestServer(t, cfg)
	im := uploadedImage(t, upload(t, s, testImage(t, "png", 2, 2), false))
	if _, err := New(cfg); err == nil {
		t.Fatal("same data directory was not locked")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = newTestServer(t, cfg)
	w := call(s, "GET", "/api/images?offset=0&limit=24", "", testToken, nil)
	var body struct {
		Data struct {
			Items    []Image `json:"items"`
			Total    int     `json:"total"`
			Bytes    int64   `json:"bytes"`
			MaxBytes int64   `json:"max_bytes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || body.Data.Total != 1 || len(body.Data.Items) != 1 || body.Data.Bytes != im.Size || body.Data.MaxBytes != cfg.MaxStorageBytes {
		t.Fatalf("bad list %d %s", w.Code, w.Body.String())
	}
	w = call(s, "DELETE", "/api/images/"+im.ID, "", testToken, nil)
	if w.Code != 200 {
		t.Fatalf("delete %d %s", w.Code, w.Body.String())
	}
	w = call(s, "GET", strings.TrimPrefix(im.URL, cfg.PublicBaseURL), "", "", nil)
	if w.Code != 404 {
		t.Fatalf("deleted image public status=%d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, im.ID)); !os.IsNotExist(err) {
		t.Fatalf("image directory left behind: %v", err)
	}
}

func TestUploadValidationLimitsAndQuota(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		s := newTestServer(t, testConfig(t))
		for _, b := range [][]byte{[]byte("<svg></svg>"), []byte("<html></html>"), {137, 80, 78, 71, 13, 10, 26, 10}} {
			if w := upload(t, s, b, false); w.Code != 415 && w.Code != 422 {
				t.Fatalf("invalid accepted %d %s", w.Code, w.Body.String())
			}
		}
	})
	t.Run("size", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.MaxUploadBytes = 32
		s := newTestServer(t, cfg)
		for _, j := range []bool{false, true} {
			if w := upload(t, s, testImage(t, "png", 2, 2), j); w.Code != 413 {
				t.Fatalf("oversize %d %s", w.Code, w.Body.String())
			}
		}
	})
	t.Run("pixels", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.MaxImagePixels = 3
		s := newTestServer(t, cfg)
		if w := upload(t, s, testImage(t, "png", 2, 2), false); w.Code != 413 {
			t.Fatalf("pixels %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("quota", func(t *testing.T) {
		data := testImage(t, "png", 2, 2)
		cfg := testConfig(t)
		cfg.MaxStorageBytes = int64(len(data))
		s := newTestServer(t, cfg)
		uploadedImage(t, upload(t, s, data, false))
		if w := upload(t, s, data, false); w.Code != 507 {
			t.Fatalf("quota %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("json data uri", func(t *testing.T) {
		s := newTestServer(t, testConfig(t))
		data := testImage(t, "png", 2, 2)
		b, _ := json.Marshal(map[string]string{"file": "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), "filename": "test.png"})
		uploadedImage(t, call(s, "POST", "/api/upload", "application/json", testToken, bytes.NewReader(b)))
	})
	t.Run("invalid base64", func(t *testing.T) {
		s := newTestServer(t, testConfig(t))
		w := call(s, "POST", "/api/upload", "application/json", testToken, strings.NewReader(`{"file":"invalid!?"}`))
		if w.Code != 400 {
			t.Fatalf("invalid base64 %d", w.Code)
		}
	})
}

func TestHealthConfigPathsAndSymlinks(t *testing.T) {
	cfg := testConfig(t)
	s := newTestServer(t, cfg)
	for _, path := range []string{"/healthz", "/readyz", "/api/config"} {
		w := call(s, "GET", path, "", "", nil)
		if w.Code != 200 {
			t.Fatalf("%s=%d", path, w.Code)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing nosniff")
		}
	}
	for _, path := range []string{"/i/../../etc/passwd", "/i/%2e%2e%2fsecret.png", "/i/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.svg", "/api/images/../secret"} {
		method := "GET"
		token := ""
		if strings.HasPrefix(path, "/api/") {
			method = "DELETE"
			token = testToken
		}
		w := call(s, method, path, "", token, nil)
		if w.Code != 404 && w.Code != 400 {
			t.Fatalf("unsafe path %s=%d", path, w.Code)
		}
	}
	im := uploadedImage(t, upload(t, s, testImage(t, "png", 2, 2), false))
	p := filepath.Join(cfg.DataDir, im.ID, "image.png")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", p); err != nil {
		t.Fatal(err)
	}
	if w := call(s, "GET", strings.TrimPrefix(im.URL, cfg.PublicBaseURL), "", "", nil); w.Code != 404 {
		t.Fatalf("symlink served %d", w.Code)
	}
}

type blockedReader struct {
	entered chan struct{}
	unblock chan struct{}
	once    sync.Once
	reader  io.Reader
}

func (r *blockedReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.entered); <-r.unblock })
	return r.reader.Read(p)
}

func TestConcurrentUploadBoundAndAtomicQuota(t *testing.T) {
	cfg := testConfig(t)
	cfg.MaxConcurrentUploads = 1
	s := newTestServer(t, cfg)
	b, ct := multipartBody(t, testImage(t, "png", 2, 2))
	r := &blockedReader{entered: make(chan struct{}), unblock: make(chan struct{}), reader: b}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- call(s, "POST", "/api/upload", ct, testToken, r) }()
	<-r.entered
	w := upload(t, s, testImage(t, "png", 2, 2), false)
	close(r.unblock)
	if w.Code != 429 {
		t.Errorf("expected bounded concurrency got %d", w.Code)
	}
	uploadedImage(t, <-done)
}

func TestListPaginationAndMalformedRequests(t *testing.T) {
	s := newTestServer(t, testConfig(t))
	for _, query := range []string{"?offset=-1", "?limit=0", "?limit=101", "?offset=no"} {
		if w := call(s, "GET", "/api/images"+query, "", testToken, nil); w.Code != 400 {
			t.Fatalf("bad query accepted %s %d", query, w.Code)
		}
	}
	for _, body := range []string{`{`, `{"file":""}`, `{"file":"YQ=="} {}`} {
		if w := call(s, "POST", "/api/upload", "application/json", testToken, strings.NewReader(body)); w.Code != 400 {
			t.Fatalf("bad json status=%d body=%q", w.Code, body)
		}
	}
	if w := call(s, "PUT", "/api/upload", "", testToken, nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method=%d", w.Code)
	}
}

func TestParallelQuotaNeverOvercommits(t *testing.T) {
	data := testImage(t, "png", 2, 2)
	cfg := testConfig(t)
	cfg.MaxConcurrentUploads = 8
	cfg.MaxStorageBytes = int64(len(data))
	s := newTestServer(t, cfg)
	var wg sync.WaitGroup
	status := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); status <- upload(t, s, data, false).Code }()
	}
	wg.Wait()
	close(status)
	ok := 0
	for code := range status {
		if code == 201 {
			ok++
		} else if code != 507 {
			t.Errorf("unexpected concurrent status %d", code)
		}
	}
	if ok != 1 {
		t.Fatalf("capacity committed %d times", ok)
	}
}

func TestRestartRejectsTamperedMetadataAndCleansIncompleteUploads(t *testing.T) {
	cfg := testConfig(t)
	s := newTestServer(t, cfg)
	im := uploadedImage(t, upload(t, s, testImage(t, "png", 2, 2), false))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cfg.DataDir, ".upload-deadbeef"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir, im.ID, "record.json"), []byte(`{"id":"../outside","mime":"image/png","size":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if recovered, err := New(cfg); err == nil {
		_ = recovered.Close()
		t.Fatal("tampered committed record accepted")
	}
}

func TestUploadChecksEntireGIFAnimation(t *testing.T) {
	cfg := testConfig(t)
	cfg.MaxImagePixels = 7
	s := newTestServer(t, cfg)
	if w := upload(t, s, animatedGIF(t, 2), false); w.Code != 422 {
		t.Fatalf("GIF cumulative pixel limit ignored: %d", w.Code)
	}
	data := animatedGIF(t, 2)
	position := lastGIFFrame(t, data) + 10
	packed := data[position-1]
	if packed&0x80 != 0 {
		position += 3 * (1 << ((packed & 7) + 1))
	}
	data[position+2] = 0xff
	if w := upload(t, newTestServer(t, testConfig(t)), data, false); w.Code != 422 {
		t.Fatalf("invalid later GIF frame accepted: %d", w.Code)
	}
}

func TestMultipartBodyLimitIncludesEpilogue(t *testing.T) {
	cfg := testConfig(t)
	cfg.MaxUploadBytes = 1024
	s := newTestServer(t, cfg)
	body, ct := multipartBody(t, testImage(t, "png", 2, 2))
	body.Write(bytes.Repeat([]byte("x"), bodyOverhead+1024))
	if w := call(s, "POST", "/api/upload", ct, testToken, body); w.Code != 413 {
		t.Fatalf("oversize multipart body accepted: %d", w.Code)
	}
}

func TestDeletionDoesNotReclaimQuotaBeforeFilesAreRemoved(t *testing.T) {
	cfg := testConfig(t)
	data := testImage(t, "png", 2, 2)
	cfg.MaxStorageBytes = int64(len(data))
	s := newTestServer(t, cfg)
	im := uploadedImage(t, upload(t, s, data, false))
	tomb := filepath.Join(cfg.DataDir, ".delete-"+im.ID)
	s.removeAll = func(string) error { return os.ErrPermission }
	w := call(s, "DELETE", "/api/images/"+im.ID, "", testToken, nil)
	if w.Code != 500 {
		t.Fatalf("failed physical cleanup reported success %d", w.Code)
	}
	if _, err := os.Stat(tomb); err != nil {
		t.Fatalf("expected retained tombstone: %v", err)
	}
	if w := upload(t, s, data, false); w.Code != 507 {
		t.Fatalf("unremoved payload quota reclaimed %d", w.Code)
	}
}

func TestReadyProbeCachesBrieflyThenRechecksDisk(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires non-root user")
	}
	cfg := testConfig(t)
	s := newTestServer(t, cfg)
	if w := call(s, "GET", "/readyz", "", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := os.Chmod(cfg.DataDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg.DataDir, 0700) })
	if w := call(s, "GET", "/readyz", "", "", nil); w.Code != 200 {
		t.Fatalf("readiness was not cached: %d", w.Code)
	}
	time.Sleep(1100 * time.Millisecond)
	if w := call(s, "GET", "/readyz", "", "", nil); w.Code != 503 {
		t.Fatalf("stale ready state after probe expiry: %d", w.Code)
	}
}

func TestNewCanonicalizesBrowserOrigin(t *testing.T) {
	cfg := testConfig(t)
	cfg.PublicBaseURL = "https://IMAGES.EXAMPLE.TEST:443/"
	s := newTestServer(t, cfg)
	r := httptest.NewRequest("GET", "/api/images", nil)
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Origin", "https://images.example.test")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("canonical browser origin rejected: %d", w.Code)
	}
}

func TestFailedDirectorySyncRollsBackUpload(t *testing.T) {
	cfg := testConfig(t)
	s := newTestServer(t, cfg)
	s.syncDirectory = func() error { return os.ErrPermission }
	if w := upload(t, s, testImage(t, "png", 2, 2), false); w.Code != 500 {
		t.Fatalf("failed sync status=%d", w.Code)
	}
	if w := call(s, "GET", "/api/images", "", testToken, nil); !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("failed upload still committed: %s", w.Body.String())
	}
	entries, err := os.ReadDir(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" {
			t.Fatalf("failed upload left %s", entry.Name())
		}
	}
}

func TestRecoveryRejectsOversizedRecord(t *testing.T) {
	cfg := testConfig(t)
	s := newTestServer(t, cfg)
	im := uploadedImage(t, upload(t, s, testImage(t, "png", 2, 2), false))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(cfg.DataDir, im.ID, "record.json")
	f, err := os.OpenFile(record, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(bytes.Repeat([]byte(" "), 17000))
	_, _ = f.Write([]byte("bad"))
	_ = f.Close()
	if recovered, err := New(cfg); err == nil {
		_ = recovered.Close()
		t.Fatal("oversized record accepted")
	}
}
