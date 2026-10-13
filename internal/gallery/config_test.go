package gallery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func configEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"LISTEN_ADDR", "PUBLIC_BASE_URL", "DATA_DIR", "UPLOAD_TOKEN", "UPLOAD_TOKEN_FILE", "MAX_UPLOAD_BYTES", "MAX_STORAGE_BYTES", "MAX_IMAGE_PIXELS", "MAX_CONCURRENT_UPLOADS"} {
		t.Setenv(key, "")
	}
	t.Setenv("PUBLIC_BASE_URL", "https://images.example.com/")
	t.Setenv("UPLOAD_TOKEN", strings.Repeat("a", 32))
}

func TestLoadConfigDefaults(t *testing.T) {
	configEnvironment(t)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != ":8080" || cfg.PublicBaseURL != "https://images.example.com" || cfg.DataDir != "/data" {
		t.Fatalf("unexpected address defaults: %+v", struct{ Listen, URL, Data string }{cfg.ListenAddr, cfg.PublicBaseURL, cfg.DataDir})
	}
	if cfg.MaxUploadBytes != 10485760 || cfg.MaxStorageBytes != 10737418240 || cfg.MaxImagePixels != 20000000 || cfg.MaxConcurrentUploads != 2 {
		t.Fatal("unexpected resource defaults")
	}
}

func TestLoadConfigTokenFile(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		t.Run(strings.ReplaceAll(suffix, "\n", "LF"), func(t *testing.T) {
			configEnvironment(t)
			token := strings.Repeat("b", 32)
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(token+suffix), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("UPLOAD_TOKEN", "")
			t.Setenv("UPLOAD_TOKEN_FILE", path)
			cfg, err := LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.UploadToken != token {
				t.Fatal("token file was not read exactly")
			}
		})
	}
}

func TestLoadConfigRejectsUnsafeValues(t *testing.T) {
	tests := []struct{ name, key, value string }{
		{"missing URL", "PUBLIC_BASE_URL", ""},
		{"URL path", "PUBLIC_BASE_URL", "https://images.example.com/images"},
		{"URL double slash", "PUBLIC_BASE_URL", "https://images.example.com//"},
		{"URL query", "PUBLIC_BASE_URL", "https://images.example.com?x=1"},
		{"URL empty query", "PUBLIC_BASE_URL", "https://images.example.com?"},
		{"URL fragment", "PUBLIC_BASE_URL", "https://images.example.com#x"},
		{"URL empty fragment", "PUBLIC_BASE_URL", "https://images.example.com#"},
		{"URL credentials", "PUBLIC_BASE_URL", "https://user:secret@images.example.com"},
		{"URL scheme", "PUBLIC_BASE_URL", "ftp://images.example.com"},
		{"URL encoded slash", "PUBLIC_BASE_URL", "https://images.example.com/%2F"},
		{"URL empty port", "PUBLIC_BASE_URL", "https://images.example.com:"},
		{"URL invalid port", "PUBLIC_BASE_URL", "https://images.example.com:65536"},
		{"URL wildcard host", "PUBLIC_BASE_URL", "https://*"},
		{"URL broken ipv6", "PUBLIC_BASE_URL", "https://[oops]"},
		{"short token", "UPLOAD_TOKEN", "sensitive-short-secret"},
		{"newline token", "UPLOAD_TOKEN", strings.Repeat("x", 32) + "\n"},
		{"unicode token", "UPLOAD_TOKEN", strings.Repeat("x", 32) + "密"},
		{"zero upload", "MAX_UPLOAD_BYTES", "0"},
		{"negative upload", "MAX_UPLOAD_BYTES", "-1"},
		{"overflow upload", "MAX_UPLOAD_BYTES", "9223372036854775808"},
		{"huge upload", "MAX_UPLOAD_BYTES", "104857601"},
		{"zero storage", "MAX_STORAGE_BYTES", "0"},
		{"huge storage", "MAX_STORAGE_BYTES", "1125899906842625"},
		{"zero pixels", "MAX_IMAGE_PIXELS", "0"},
		{"huge pixels", "MAX_IMAGE_PIXELS", "100000001"},
		{"zero concurrency", "MAX_CONCURRENT_UPLOADS", "0"},
		{"huge concurrency", "MAX_CONCURRENT_UPLOADS", "33"},
		{"noninteger concurrency", "MAX_CONCURRENT_UPLOADS", "1.5"},
		{"listen no port", "LISTEN_ADDR", "localhost"},
		{"listen zero port", "LISTEN_ADDR", ":0"},
		{"listen huge port", "LISTEN_ADDR", ":65536"},
		{"relative data", "DATA_DIR", "relative"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configEnvironment(t)
			t.Setenv(tc.key, tc.value)
			_, err := LoadConfig()
			if err == nil {
				t.Fatal("invalid configuration was accepted")
			}
			if tc.key == "UPLOAD_TOKEN" && strings.Contains(err.Error(), tc.value) {
				t.Fatal("error exposed secret")
			}
		})
	}
}

func TestLoadConfigTokenSources(t *testing.T) {
	configEnvironment(t)
	t.Setenv("UPLOAD_TOKEN_FILE", "/does/not/exist")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("both token sources accepted")
	}
	t.Setenv("UPLOAD_TOKEN", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("unreadable token file accepted")
	}
	t.Setenv("UPLOAD_TOKEN_FILE", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("no token source accepted")
	}
}

func TestLoadConfigRejectsExtraTokenFileNewlines(t *testing.T) {
	configEnvironment(t)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(strings.Repeat("z", 32)+"\n\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPLOAD_TOKEN", "")
	t.Setenv("UPLOAD_TOKEN_FILE", path)
	if _, err := LoadConfig(); err == nil {
		t.Fatal("extra token file newlines accepted")
	}
}

func TestLoadConfigRejectsBareTokenFileCarriageReturn(t *testing.T) {
	configEnvironment(t)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(strings.Repeat("z", 32)+"\r"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPLOAD_TOKEN", "")
	t.Setenv("UPLOAD_TOKEN_FILE", path)
	if _, err := LoadConfig(); err == nil {
		t.Fatal("bare carriage return accepted")
	}
}

func TestConfigValidateChecksDirectConstruction(t *testing.T) {
	configEnvironment(t)
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxUploadBytes = 104857601
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid direct configuration accepted")
	}
}

func TestLoadConfigAcceptsLocalAndIPv6Origins(t *testing.T) {
	for _, origin := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "https://[::1]:8443"} {
		t.Run(origin, func(t *testing.T) {
			configEnvironment(t)
			t.Setenv("PUBLIC_BASE_URL", origin)
			if _, err := LoadConfig(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadConfigCanonicalizesBrowserOrigin(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"HTTPS://IMAGES.Example.COM:443/", "https://images.example.com"},
		{"HTTP://LOCALHOST:080/", "http://localhost"},
		{"https://images.example.com:0443", "https://images.example.com"},
		{"https://IMAGES.example.com:08443/", "https://images.example.com:8443"},
		{"http://[2001:0DB8:0:0:0:0:0:1]:080/", "http://[2001:db8::1]"},
		{"http://[::ffff:127.0.0.1]:8080", "http://[::ffff:7f00:1]:8080"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			configEnvironment(t)
			t.Setenv("PUBLIC_BASE_URL", tc.input)
			cfg, err := LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.PublicBaseURL != tc.want {
				t.Fatalf("got %q, want browser origin %q", cfg.PublicBaseURL, tc.want)
			}
		})
	}
}
