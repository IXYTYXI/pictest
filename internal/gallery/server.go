package gallery

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed web/*
var webFiles embed.FS

type Image struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename"`
	URL       string    `json:"url"`
	MIME      string    `json:"mime"`
	Size      int64     `json:"size"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	CreatedAt time.Time `json:"created_at"`
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func apiError(w http.ResponseWriter, status int, code, message string) {
	jsonResponse(w, status, map[string]any{"success": false, "error": map[string]string{"code": code, "message": message}})
}
func success(w http.ResponseWriter, status int, data any) {
	jsonResponse(w, status, map[string]any{"success": true, "data": data})
}
func (s *Server) authenticated(w http.ResponseWriter, r *http.Request) bool {
	values := r.Header.Values("Authorization")
	provided := ""
	if len(values) == 1 && strings.HasPrefix(values[0], "Bearer ") {
		provided = strings.TrimPrefix(values[0], "Bearer ")
	}
	a, b := sha256.Sum256([]byte(provided)), sha256.Sum256([]byte(s.cfg.UploadToken))
	if subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		apiError(w, 401, "unauthorized", "需要有效的上传令牌")
		return false
	}
	origin := r.Header.Values("Origin")
	if len(origin) > 1 || (len(origin) == 1 && origin[0] != s.cfg.PublicBaseURL) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		apiError(w, 403, "forbidden_origin", "不允许跨站访问管理接口")
		return false
	}
	return true
}
func methodAllowed(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	apiError(w, 405, "method_not_allowed", "不支持此请求方法")
	return false
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' blob:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
	}
	switch {
	case path == "/healthz":
		if !methodAllowed(w, r, "GET", "HEAD") {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		if r.Method == "GET" {
			_, _ = io.WriteString(w, "ok\n")
		}
	case path == "/readyz":
		if !methodAllowed(w, r, "GET", "HEAD") {
			return
		}
		if err := s.ready(); err != nil {
			apiError(w, 503, "storage_unavailable", "存储暂不可写")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		if r.Method == "GET" {
			_, _ = io.WriteString(w, "ready\n")
		}
	case path == "/api/config":
		if !methodAllowed(w, r, "GET") {
			return
		}
		success(w, 200, map[string]any{"max_upload_bytes": s.cfg.MaxUploadBytes, "formats": []string{"image/png", "image/jpeg", "image/gif"}})
	case path == "/api/upload":
		if !s.authenticated(w, r) || !methodAllowed(w, r, "POST") {
			return
		}
		s.upload(w, r)
	case path == "/api/images":
		if !s.authenticated(w, r) || !methodAllowed(w, r, "GET") {
			return
		}
		s.list(w, r)
	case strings.HasPrefix(path, "/api/images/"):
		if !s.authenticated(w, r) || !methodAllowed(w, r, "DELETE") {
			return
		}
		s.delete(w, r)
	case strings.HasPrefix(path, "/i/"):
		if !methodAllowed(w, r, "GET", "HEAD") {
			return
		}
		s.serveImage(w, r)
	case path == "/" || path == "/app.js" || path == "/style.css":
		if !methodAllowed(w, r, "GET", "HEAD") {
			return
		}
		s.serveAsset(w, r)
	default:
		apiError(w, 404, "not_found", "资源不存在")
	}
}
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	data, err := webFiles.ReadFile("web/" + name)
	if err != nil {
		apiError(w, 404, "not_found", "资源不存在")
		return
	}
	types := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "style.css": "text/css; charset=utf-8"}
	w.Header().Set("Content-Type", types[name])
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method == "GET" {
		_, _ = w.Write(data)
	}
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	parse := func(name string, defaultValue, min, max int) (int, bool) {
		value := r.URL.Query().Get(name)
		if value == "" {
			return defaultValue, true
		}
		n, err := strconv.Atoi(value)
		return n, err == nil && n >= min && n <= max
	}
	offset, ok := parse("offset", 0, 0, int(^uint(0)>>1))
	if !ok {
		apiError(w, 400, "invalid_pagination", "offset 必须为非负整数")
		return
	}
	limit, ok := parse("limit", 24, 1, 100)
	if !ok {
		apiError(w, 400, "invalid_pagination", "limit 必须为 1 到 100")
		return
	}
	s.mu.RLock()
	items := make([]Image, 0, len(s.images))
	for _, im := range s.images {
		items = append(items, im)
	}
	totalBytes := s.bytes
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	total := len(items)
	if offset > total {
		offset = total
	}
	end := total
	if limit < total-offset {
		end = offset + limit
	}
	success(w, 200, map[string]any{"items": items[offset:end], "total": total, "bytes": totalBytes, "max_bytes": s.cfg.MaxStorageBytes})
}
func (s *Server) serveImage(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/i/")
	parts := strings.Split(name, ".")
	if len(parts) != 2 || !validID(parts[0]) {
		apiError(w, 404, "not_found", "图片不存在")
		return
	}
	s.mu.RLock()
	im, exists := s.images[parts[0]]
	if !exists || imageExtension(im.MIME) != parts[1] {
		s.mu.RUnlock()
		apiError(w, 404, "not_found", "图片不存在")
		return
	}
	f, err := s.openImage(im)
	s.mu.RUnlock()
	if err != nil {
		apiError(w, 404, "not_found", "图片不存在")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", im.MIME)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Disposition", "inline; filename=\""+im.ID+"."+parts[1]+"\"")
	w.Header().Set("ETag", "\""+im.ID+"\"")
	http.ServeContent(w, r, im.ID+"."+parts[1], im.CreatedAt, f)
}
func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/images/")
	if !validID(id) {
		apiError(w, 404, "not_found", "图片不存在")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	im, exists := s.images[id]
	if !exists {
		apiError(w, 404, "not_found", "图片不存在")
		return
	}
	if err := s.root.Rename(id, ".delete-"+id); err != nil {
		apiError(w, 500, "storage_error", "删除图片失败")
		return
	}
	delete(s.images, id)
	// Tombstones are excluded from reads immediately, and retried during startup.
	if err := s.removeAll(".delete-" + id); err != nil {
		apiError(w, 500, "storage_error", "图片已移除但磁盘清理失败，请检查存储并重启服务")
		return
	}
	s.bytes -= im.Size
	_ = s.syncRoot()
	jsonResponse(w, 200, map[string]bool{"success": true})
}
