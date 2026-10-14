package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "atlas/rs/gen/rs"
	"atlas/rs/server"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

func TestTLSMultiplexesLoginAndGRPC(t *testing.T) {
	dir := t.TempDir()
	state, _, err := server.InitializeAuth(
		filepath.Join(dir, "auth.json"),
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := server.LoadAuthManager(filepath.Join(dir, "auth.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	rsServer := server.New(server.Config{Auth: auth, MaxUploadSize: 1 << 20})
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(rsServer.UnaryInterceptor()),
		grpc.StreamInterceptor(rsServer.StreamInterceptor()),
	)
	pb.RegisterRsServer(grpcServer, rsServer)
	authHandler := auth.HTTPHandler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		authHandler.ServeHTTP(w, r)
	})
	tlsServer := httptest.NewUnstartedServer(handler)
	tlsServer.EnableHTTP2 = true
	tlsServer.StartTLS()
	defer tlsServer.Close()

	node := Node{
		Name:     "tls",
		URL:      tlsServer.URL + "/r/" + state.AccountID,
		Password: "correct-horse-battery-staple",
	}
	useTestHTTPClient(t, tlsServer)
	session, _, err := authenticateNode(context.Background(), node)
	if err != nil {
		t.Fatal(err)
	}

	serverURL, err := url.Parse(tlsServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(tlsServer.Certificate())
	conn, err := grpc.NewClient(
		serverURL.Host,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			ServerName: serverURL.Hostname(),
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pb.NewRsClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+session.Token)

	health, err := client.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if health.Version != server.Version {
		t.Fatalf("version = %s, want %s", health.Version, server.Version)
	}
	var stdout, stderr bytes.Buffer
	code, err := runShell(ctx, client, "printf tls-ok", 5, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || stdout.String() != "tls-ok" || stderr.Len() != 0 {
		t.Fatalf("shell code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestH2CMultiplexesLoginAndGRPC(t *testing.T) {
	dir := t.TempDir()
	state, _, err := server.InitializeAuth(
		filepath.Join(dir, "auth.json"),
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := server.LoadAuthManager(filepath.Join(dir, "auth.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	rsServer := server.New(server.Config{Auth: auth, MaxUploadSize: 1 << 20})
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(rsServer.UnaryInterceptor()),
		grpc.StreamInterceptor(rsServer.StreamInterceptor()),
	)
	pb.RegisterRsServer(grpcServer, rsServer)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
			return
		}
		auth.HTTPHandler().ServeHTTP(w, r)
	})
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	httpServer := httptest.NewUnstartedServer(handler)
	httpServer.Config.Protocols = protocols
	httpServer.Start()
	defer httpServer.Close()

	node := Node{
		Name:     "h2c",
		URL:      httpServer.URL + "/r/" + state.AccountID,
		Password: "correct-horse-battery-staple",
	}
	session, _, err := authenticateNode(context.Background(), node)
	if err != nil {
		t.Fatal(err)
	}
	serverURL, err := url.Parse(httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(serverURL.Host, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := pb.NewRsClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+session.Token)
	health, err := client.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if health.Version != server.Version {
		t.Fatalf("version = %s, want %s", health.Version, server.Version)
	}
}
