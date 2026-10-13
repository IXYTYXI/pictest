package gallery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Server struct {
	cfg           Config
	root          *os.Root
	lock          *os.File
	mu            sync.RWMutex
	images        map[string]Image
	bytes         int64
	uploadSlots   chan struct{}
	closed        bool
	removeAll     func(string) error
	syncDirectory func() error
	readyMu       sync.Mutex
	readyAt       time.Time
	readyErr      error
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func imageExtension(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	}
	return ""
}
func New(cfg Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.PublicBaseURL, _ = canonicalOrigin(cfg.PublicBaseURL)
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, errors.New("cannot create data directory")
	}
	info, err := os.Lstat(cfg.DataDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("data directory must be a real directory")
	}
	root, err := os.OpenRoot(cfg.DataDir)
	if err != nil {
		return nil, errors.New("cannot open data directory")
	}
	s := &Server{cfg: cfg, root: root, images: make(map[string]Image), uploadSlots: make(chan struct{}, cfg.MaxConcurrentUploads), removeAll: root.RemoveAll}
	s.syncDirectory = s.syncRoot
	s.lock, err = root.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		_ = root.Close()
		return nil, errors.New("cannot open data directory lock")
	}
	lockInfo, err := s.lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() {
		_ = s.Close()
		return nil, errors.New("invalid data directory lock")
	}
	if err = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = s.lock.Close()
		_ = root.Close()
		return nil, errors.New("data directory is already in use")
	}
	if err = s.recover(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		err = s.lock.Close()
	}
	if s.root != nil {
		if e := s.root.Close(); err == nil {
			err = e
		}
	}
	return err
}
func (s *Server) openRegular(name string) (*os.File, error) {
	info, err := s.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("not a regular stored file")
	}
	f, err := s.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("not a regular stored file")
	}
	return f, nil
}
func (s *Server) openImage(im Image) (*os.File, error) {
	info, err := s.root.Lstat(im.ID)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("invalid image directory")
	}
	f, err := s.openRegular(im.ID + "/image." + imageExtension(im.MIME))
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err != nil || info.Size() != im.Size {
		_ = f.Close()
		return nil, errors.New("stored image size mismatch")
	}
	return f, nil
}
func (s *Server) recover() error {
	f, err := s.root.Open(".")
	if err != nil {
		return errors.New("cannot scan storage")
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return errors.New("cannot scan storage")
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".upload-") || strings.HasPrefix(name, ".delete-") || strings.HasPrefix(name, ".ready-") {
			if err := s.root.RemoveAll(name); err != nil {
				return errors.New("cannot clean incomplete storage operation")
			}
			continue
		}
		if !validID(name) {
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("invalid image directory in storage")
		}
		r, err := s.openRegular(name + "/record.json")
		if err != nil {
			return errors.New("invalid image metadata in storage")
		}
		info, err := r.Stat()
		if err != nil || info.Size() > 16384 {
			_ = r.Close()
			return errors.New("image metadata in storage exceeds size limit")
		}
		dec := json.NewDecoder(io.LimitReader(r, 16385))
		var im Image
		err = dec.Decode(&im)
		if err == nil {
			var extra any
			if dec.Decode(&extra) != io.EOF {
				err = errors.New("trailing metadata")
			}
		}
		_ = r.Close()
		if err != nil || im.ID != name || imageExtension(im.MIME) == "" || im.Size <= 0 || im.Width <= 0 || im.Height <= 0 || im.CreatedAt.IsZero() {
			return errors.New("invalid image metadata in storage")
		}
		data, err := s.openImage(im)
		if err != nil {
			return errors.New("invalid image file in storage")
		}
		ic, format, err := image.DecodeConfig(data)
		_ = data.Close()
		if err != nil || "image/"+format != im.MIME || ic.Width != im.Width || ic.Height != im.Height {
			return errors.New("stored image does not match metadata")
		}
		if im.Size > 1<<50-s.bytes {
			return errors.New("stored bytes exceed accounting bounds")
		}
		im.URL = s.cfg.PublicBaseURL + "/i/" + name + "." + imageExtension(im.MIME)
		s.images[name] = im
		s.bytes += im.Size
	}
	return nil
}
func (s *Server) syncRoot() error {
	f, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (s *Server) ready() (result error) {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return errors.New("closed")
	}
	if !s.readyAt.IsZero() && time.Since(s.readyAt) < time.Second {
		return s.readyErr
	}
	defer func() { s.readyAt = time.Now(); s.readyErr = result }()
	id, err := randomID()
	if err != nil {
		return err
	}
	name := ".ready-" + id
	f, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer s.root.Remove(name)
	_, err = f.Write([]byte("ready"))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
func (s *Server) commit(tmp string, im Image) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("storage closed")
	}
	if im.Size > s.cfg.MaxStorageBytes-s.bytes {
		return errQuota
	}
	if _, exists := s.images[im.ID]; exists {
		return errors.New("image ID collision")
	}
	if _, err := s.root.Lstat(im.ID); !os.IsNotExist(err) {
		return errors.New("image ID collision")
	}
	metadata, err := json.Marshal(im)
	if err != nil {
		return err
	}
	f, err := s.root.OpenFile(tmp+"/record.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(metadata); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	dir, err := s.root.Open(tmp)
	if err != nil {
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		return err
	}
	if err = s.root.Rename(tmp, im.ID); err != nil {
		return err
	}
	if err = s.syncDirectory(); err != nil {
		// Revert a failed durable commit while the index is still locked. If
		// the filesystem also refuses rollback, keep accounting consistent
		// with the committed directory rather than hiding its disk usage.
		if rollbackErr := s.root.Rename(im.ID, tmp); rollbackErr != nil {
			s.images[im.ID] = im
			s.bytes += im.Size
		}
		return fmt.Errorf("storage sync failed: %w", err)
	}
	s.images[im.ID] = im
	s.bytes += im.Size
	return nil
}

var errQuota = errors.New("storage quota exceeded")
