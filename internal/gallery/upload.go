package gallery

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const bodyOverhead = 64 * 1024

type uploadError struct {
	status        int
	code, message string
}

func (e *uploadError) Error() string     { return e.message }
func invalidUpload(message string) error { return &uploadError{400, "invalid_upload", message} }
func uploadTooLarge() error {
	return &uploadError{413, "upload_too_large", "图片或请求体超过大小限制"}
}
func cleanFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	runes := []rune(strings.TrimSpace(name))
	if len(runes) > 200 {
		runes = runes[:200]
	}
	name = string(runes)
	if name == "" || name == "." || name == ".." || name == "/" {
		return "image"
	}
	return name
}
func (s *Server) copyUpload(dst io.Writer, src io.Reader) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, s.cfg.MaxUploadBytes+1))
	if n > s.cfg.MaxUploadBytes {
		return 0, uploadTooLarge()
	}
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return 0, uploadTooLarge()
		}
		return 0, invalidUpload("无法读取完整的上传内容")
	}
	if n == 0 {
		return 0, invalidUpload("图片不能为空")
	}
	return n, nil
}
func (s *Server) receive(w http.ResponseWriter, r *http.Request, f *os.File) (string, int64, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return "", 0, invalidUpload("Content-Type 无效")
	}
	switch mediaType {
	case "multipart/form-data":
		if r.ContentLength > s.cfg.MaxUploadBytes+bodyOverhead {
			return "", 0, uploadTooLarge()
		}
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+bodyOverhead)
		reader, err := r.MultipartReader()
		if err != nil {
			return "", 0, invalidUpload("multipart 内容无效")
		}
		var filename string
		var size int64
		found := false
		parts := 0
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					return "", 0, uploadTooLarge()
				}
				return "", 0, invalidUpload("multipart 内容不完整")
			}
			parts++
			if parts > 16 {
				return "", 0, invalidUpload("multipart 字段过多")
			}
			if part.FormName() == "file" {
				if found {
					_ = part.Close()
					return "", 0, invalidUpload("只能上传一个 file 文件")
				}
				found = true
				filename = cleanFilename(part.FileName())
				size, err = s.copyUpload(f, part)
			} else {
				_, err = io.Copy(io.Discard, part)
			}
			_ = part.Close()
			if err != nil {
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					return "", 0, uploadTooLarge()
				}
				return "", 0, err
			}
		}
		if !found {
			return "", 0, invalidUpload("缺少 file 文件字段")
		}
		// Multipart allows an epilogue after the final boundary. Account for it
		// even when the body is chunked and has no declared Content-Length.
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				return "", 0, uploadTooLarge()
			}
			return "", 0, invalidUpload("multipart 内容不完整")
		}
		return filename, size, nil
	case "application/json":
		if r.ContentLength > ((s.cfg.MaxUploadBytes+2)/3)*4+bodyOverhead {
			return "", 0, uploadTooLarge()
		}
		r.Body = http.MaxBytesReader(w, r.Body, ((s.cfg.MaxUploadBytes+2)/3)*4+bodyOverhead)
		var payload struct {
			File     string `json:"file"`
			Filename string `json:"filename"`
		}
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&payload); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				return "", 0, uploadTooLarge()
			}
			return "", 0, invalidUpload("JSON 内容无效")
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				return "", 0, uploadTooLarge()
			}
			return "", 0, invalidUpload("JSON 后存在额外内容")
		}
		encoded := payload.File
		if strings.HasPrefix(encoded, "data:") {
			comma := strings.IndexByte(encoded, ',')
			if comma < 0 || !strings.HasSuffix(encoded[:comma], ";base64") {
				return "", 0, invalidUpload("Data URI 必须使用 Base64 编码")
			}
			encoded = encoded[comma+1:]
		}
		if encoded == "" {
			return "", 0, invalidUpload("缺少 file Base64 内容")
		}
		if int64(len(encoded)) > ((s.cfg.MaxUploadBytes+2)/3)*4 {
			return "", 0, uploadTooLarge()
		}
		size, err := s.copyUpload(f, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded)))
		return cleanFilename(payload.Filename), size, err
	default:
		return "", 0, &uploadError{415, "unsupported_content_type", "请使用 multipart/form-data 或 application/json"}
	}
}
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	select {
	case s.uploadSlots <- struct{}{}:
		defer func() { <-s.uploadSlots }()
	default:
		w.Header().Set("Retry-After", "2")
		apiError(w, 429, "too_many_uploads", "上传并发已满，请稍后重试")
		return
	}
	id, err := randomID()
	if err != nil {
		apiError(w, 500, "storage_error", "无法创建图片标识")
		return
	}
	tmp := ".upload-" + id
	if err = s.root.Mkdir(tmp, 0700); err != nil {
		apiError(w, 500, "storage_error", "无法创建上传目录")
		return
	}
	defer s.root.RemoveAll(tmp)
	f, err := s.root.OpenFile(tmp+"/upload", os.O_CREATE|os.O_EXCL|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		apiError(w, 500, "storage_error", "无法写入图片")
		return
	}
	defer f.Close()
	filename, size, err := s.receive(w, r, f)
	if err != nil {
		var e *uploadError
		if errors.As(err, &e) {
			apiError(w, e.status, e.code, e.message)
		} else {
			apiError(w, 400, "invalid_upload", "上传内容无效")
		}
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		apiError(w, 500, "storage_error", "无法读取图片")
		return
	}
	ic, format, err := image.DecodeConfig(f)
	if err != nil || !(format == "png" || format == "jpeg" || format == "gif") {
		apiError(w, 415, "unsupported_image", "仅支持有效的 PNG、JPEG、GIF 图片")
		return
	}
	if ic.Width <= 0 || ic.Height <= 0 || int64(ic.Width) > s.cfg.MaxImagePixels/int64(ic.Height) {
		apiError(w, 413, "image_too_large", "图片像素数量超过限制")
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		apiError(w, 500, "storage_error", "无法读取图片")
		return
	}
	if format == "gif" {
		if err := validateGIF(f, s.cfg.MaxImagePixels); err != nil {
			apiError(w, 422, "invalid_image", "GIF 内容无效、累计帧像素超限或超过 1000 帧")
			return
		}
	} else {
		decoded, decodedFormat, err := image.Decode(f)
		if err != nil || decodedFormat != format || decoded.Bounds().Dx() != ic.Width || decoded.Bounds().Dy() != ic.Height {
			apiError(w, 422, "invalid_image", "图片内容损坏或不完整")
			return
		}
	}
	if err = f.Sync(); err != nil {
		apiError(w, 500, "storage_error", "无法保存图片")
		return
	}
	if err = f.Close(); err != nil {
		apiError(w, 500, "storage_error", "无法保存图片")
		return
	}
	im := Image{ID: id, Filename: filename, MIME: "image/" + format, Size: size, Width: ic.Width, Height: ic.Height, CreatedAt: time.Now().UTC()}
	ext := imageExtension(im.MIME)
	im.URL = s.cfg.PublicBaseURL + "/i/" + id + "." + ext
	if err = s.root.Rename(tmp+"/upload", tmp+"/image."+ext); err != nil {
		apiError(w, 500, "storage_error", "无法保存图片")
		return
	}
	if err = s.commit(tmp, im); err != nil {
		if errors.Is(err, errQuota) {
			apiError(w, 507, "storage_full", "存储容量已满，请先删除图片")
		} else {
			apiError(w, 500, "storage_error", "保存图片失败")
		}
		return
	}
	success(w, 201, im)
}
