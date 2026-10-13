package gallery

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config contains the server's validated runtime settings. UploadToken is secret
// and must never be included in logs or error messages.
type Config struct {
	ListenAddr           string
	PublicBaseURL        string
	DataDir              string
	UploadToken          string
	MaxUploadBytes       int64
	MaxStorageBytes      int64
	MaxImagePixels       int64
	MaxConcurrentUploads int
}

const maxTokenLength = 4096

// LoadConfig reads environment settings and requires exactly one token source.
func LoadConfig() (Config, error) {
	cfg := Config{
		ListenAddr:    envDefault("LISTEN_ADDR", ":8080"),
		PublicBaseURL: os.Getenv("PUBLIC_BASE_URL"),
		DataDir:       envDefault("DATA_DIR", "/data"),
		UploadToken:   os.Getenv("UPLOAD_TOKEN"),
	}
	tokenFile := os.Getenv("UPLOAD_TOKEN_FILE")
	if (cfg.UploadToken == "") == (tokenFile == "") {
		return Config{}, errors.New("set exactly one of UPLOAD_TOKEN or UPLOAD_TOKEN_FILE")
	}
	if tokenFile != "" {
		file, err := os.Open(tokenFile)
		if err != nil {
			return Config{}, errors.New("could not read UPLOAD_TOKEN_FILE")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxTokenLength+3))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return Config{}, errors.New("could not read UPLOAD_TOKEN_FILE")
		}
		// keygen prints a single newline. Other whitespace remains significant.
		cfg.UploadToken = string(data)
		if strings.HasSuffix(cfg.UploadToken, "\n") {
			cfg.UploadToken = strings.TrimSuffix(cfg.UploadToken, "\n")
			cfg.UploadToken = strings.TrimSuffix(cfg.UploadToken, "\r")
		}
	}
	var err error
	if cfg.MaxUploadBytes, err = envInt("MAX_UPLOAD_BYTES", 10<<20); err != nil {
		return Config{}, err
	}
	if cfg.MaxStorageBytes, err = envInt("MAX_STORAGE_BYTES", 10<<30); err != nil {
		return Config{}, err
	}
	if cfg.MaxImagePixels, err = envInt("MAX_IMAGE_PIXELS", 20000000); err != nil {
		return Config{}, err
	}
	concurrency, err := envInt("MAX_CONCURRENT_UPLOADS", 2)
	if err != nil {
		return Config{}, err
	}
	if concurrency < 1 || concurrency > 32 {
		return Config{}, errors.New("MAX_CONCURRENT_UPLOADS must be between 1 and 32")
	}
	cfg.MaxConcurrentUploads = int(concurrency)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	cfg.PublicBaseURL, err = canonicalOrigin(cfg.PublicBaseURL)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate also protects callers constructing Config without LoadConfig.
func (cfg Config) Validate() error {
	if _, err := validOrigin(cfg.PublicBaseURL); err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil || !validPort(port) {
		return errors.New("LISTEN_ADDR must contain a host and a port between 1 and 65535")
	}
	if !filepath.IsAbs(cfg.DataDir) || strings.ContainsRune(cfg.DataDir, 0) {
		return errors.New("DATA_DIR must be an absolute path")
	}
	if len(cfg.UploadToken) < 32 || len(cfg.UploadToken) > maxTokenLength {
		return errors.New("upload token must contain 32 to 4096 printable ASCII characters")
	}
	for _, b := range []byte(cfg.UploadToken) {
		if b < 32 || b > 126 {
			return errors.New("upload token must contain 32 to 4096 printable ASCII characters")
		}
	}
	if cfg.MaxUploadBytes < 1 || cfg.MaxUploadBytes > 100<<20 {
		return errors.New("MAX_UPLOAD_BYTES must be between 1 and 104857600")
	}
	if cfg.MaxStorageBytes < 1 || cfg.MaxStorageBytes > 1<<50 {
		return errors.New("MAX_STORAGE_BYTES must be between 1 and 1125899906842624")
	}
	if cfg.MaxImagePixels < 1 || cfg.MaxImagePixels > 100000000 {
		return errors.New("MAX_IMAGE_PIXELS must be between 1 and 100000000")
	}
	if cfg.MaxConcurrentUploads < 1 || cfg.MaxConcurrentUploads > 32 {
		return errors.New("MAX_CONCURRENT_UPLOADS must be between 1 and 32")
	}
	return nil
}

func validOrigin(raw string) (*url.URL, error) {
	invalid := errors.New("PUBLIC_BASE_URL must be an http or https origin without credentials, path, query, or fragment")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return nil, invalid
	}
	host := u.Hostname()
	if host == "" {
		return nil, invalid
	}
	if strings.HasPrefix(u.Host, "[") {
		if net.ParseIP(host) == nil {
			return nil, invalid
		}
	} else if !validDNSHost(host) {
		return nil, invalid
	}
	if u.Port() != "" {
		if !validPort(u.Port()) {
			return nil, invalid
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, invalid
	}
	return u, nil
}

// canonicalOrigin matches the scheme, host, and port serialization browsers
// use for Origin headers, so valid configuration does not reject same-site UI.
func canonicalOrigin(raw string) (string, error) {
	u, err := validOrigin(raw)
	if err != nil {
		return "", err
	}
	host := strings.ToLower(u.Hostname())
	ipv6 := strings.HasPrefix(u.Host, "[")
	if ipv6 {
		address, err := netip.ParseAddr(host)
		if err != nil {
			return "", errors.New("PUBLIC_BASE_URL contains an invalid IPv6 host")
		}
		host = address.String()
		if address.Is4In6() {
			v4 := address.As4()
			host = fmt.Sprintf("::ffff:%x:%x", uint16(v4[0])<<8|uint16(v4[1]), uint16(v4[2])<<8|uint16(v4[3]))
		}
	}
	port := u.Port()
	if port != "" {
		number, _ := strconv.Atoi(port) // validOrigin has checked the range.
		port = strconv.Itoa(number)
		if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
			port = ""
		}
	}
	u.Host = host
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if ipv6 {
		u.Host = "[" + host + "]"
	}
	u.Path = ""
	return u.String(), nil
}

func validDNSHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func validPort(raw string) bool {
	if raw == "" {
		return false
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	port, err := strconv.Atoi(raw)
	return err == nil && port >= 1 && port <= 65535
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int64) (int64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer within the supported range", key)
	}
	return value, nil
}
