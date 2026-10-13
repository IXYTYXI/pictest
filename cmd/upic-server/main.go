package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"upic-server/internal/gallery"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: upic-server [serve|keygen|healthcheck]")
		return 1
	}
	command := "serve"
	if len(args) == 1 {
		command = args[0]
	}
	switch command {
	case "keygen":
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			fmt.Fprintln(stderr, "could not generate upload token")
			return 1
		}
		if _, err := fmt.Fprintln(stdout, hex.EncodeToString(key)); err != nil {
			fmt.Fprintln(stderr, "could not write upload token")
			return 1
		}
		return 0
	case "healthcheck":
		if err := healthcheck(ctx); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "serve":
		cfg, err := gallery.LoadConfig()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := serve(ctx, cfg, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(stderr, "usage: upic-server [serve|keygen|healthcheck]")
		return 1
	}
}

func serve(ctx context.Context, cfg gallery.Config, stderr io.Writer) (result error) {
	app, err := gallery.New(cfg)
	if err != nil {
		return fmt.Errorf("could not initialize gallery: %w", err)
	}
	defer func() { result = errors.Join(result, app.Close()) }()
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("could not listen: %w", err)
	}
	server := &http.Server{
		Handler:           app,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(stderr, "http: ", log.LstdFlags),
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	fmt.Fprintf(stderr, "upic-server listening on %s\n", listener.Addr())
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			<-serveErr
			return errors.New("graceful shutdown timed out; active connections were closed")
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func healthcheck(ctx context.Context) error {
	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}
	endpoint, err := healthcheckURL(listenAddr)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("could not build healthcheck request")
	}
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Timeout:       3 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("healthcheck request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("server is not ready")
	}
	return nil
}

func healthcheckURL(listenAddr string) (string, error) {
	_, port, err := net.SplitHostPort(listenAddr)
	invalid := errors.New("LISTEN_ADDR must contain a port between 1 and 65535")
	if err != nil {
		return "", invalid
	}
	for _, ch := range port {
		if ch < '0' || ch > '9' {
			return "", invalid
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", invalid
	}
	return "http://" + net.JoinHostPort("127.0.0.1", port) + "/readyz", nil
}
