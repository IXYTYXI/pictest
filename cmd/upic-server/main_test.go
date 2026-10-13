package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunKeygenWithoutConfiguration(t *testing.T) {
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("UPLOAD_TOKEN", "")
	var previous string
	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"keygen"}, &stdout, &stderr); code != 0 {
			t.Fatalf("keygen failed: %s", stderr.String())
		}
		line := stdout.String()
		if len(line) != 65 || !strings.HasSuffix(line, "\n") {
			t.Fatal("keygen must emit 64 hex digits and one newline")
		}
		key, err := hex.DecodeString(strings.TrimSuffix(line, "\n"))
		if err != nil || len(key) != 32 {
			t.Fatal("keygen did not produce a 256-bit hex key")
		}
		if previous == line {
			t.Fatal("keygen repeated its output")
		}
		if stderr.Len() != 0 {
			t.Fatal("keygen wrote errors")
		}
		previous = line
	}
}

func TestRunRejectsUnknownOrExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"unknown"}, {"keygen", "extra"}, {"healthcheck", "extra"}, {"serve", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code == 0 {
			t.Fatal("invalid arguments accepted")
		}
		if stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatal("invalid arguments should only report an error")
		}
	}
}

func TestHealthcheckRequiresReadyStatusWithoutConfiguration(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/readyz" {
					t.Errorf("unexpected healthcheck path: %s", r.URL.Path)
				}
				if status == http.StatusFound {
					w.Header().Set("Location", "/redirected")
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			t.Setenv("LISTEN_ADDR", strings.TrimPrefix(server.URL, "http://"))
			t.Setenv("PUBLIC_BASE_URL", "")
			t.Setenv("UPLOAD_TOKEN", "")
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"healthcheck"}, &stdout, &stderr)
			if (code == 0) != (status == http.StatusOK) {
				t.Fatalf("unexpected healthcheck result %d", code)
			}
			if requests != 1 {
				t.Fatalf("healthcheck made %d requests", requests)
			}
		})
	}
}

func TestHealthcheckURLUsesLocalPort(t *testing.T) {
	for _, addr := range []string{":8090", "0.0.0.0:8090", "192.168.99.9:8090", "[::]:8090"} {
		got, err := healthcheckURL(addr)
		if err != nil {
			t.Fatal(err)
		}
		if got != "http://127.0.0.1:8090/readyz" {
			t.Fatalf("unexpected URL: %s", got)
		}
	}
	for _, addr := range []string{"localhost", ":0", ":65536", ":http", ":+80"} {
		if _, err := healthcheckURL(addr); err == nil {
			t.Fatalf("invalid address %q accepted", addr)
		}
	}
}

func TestRunConfigurationErrorDoesNotExposeToken(t *testing.T) {
	t.Setenv("UPLOAD_TOKEN", "sensitive-short-secret")
	t.Setenv("UPLOAD_TOKEN_FILE", "")
	t.Setenv("PUBLIC_BASE_URL", "https://images.example.com")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), nil, &stdout, &stderr); code == 0 {
		t.Fatal("invalid configuration accepted")
	}
	if strings.Contains(stderr.String(), "sensitive-short-secret") {
		t.Fatal("error exposed secret")
	}
}
