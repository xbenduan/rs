package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestUnaryInterceptorRequiresSessionForHealth(t *testing.T) {
	dir := t.TempDir()
	state, _, err := InitializeAuth(
		filepath.Join(dir, "auth.json"),
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := LoadAuthManager(filepath.Join(dir, "auth.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{Auth: auth})
	interceptor := srv.UnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/rs.Rs/Health"}
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	if _, err := interceptor(context.Background(), nil, info, handler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated health error = %v", err)
	}

	session, err := auth.Login(state.AccountID, "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("authorization", "Bearer "+session.Token),
	)
	result, err := interceptor(ctx, nil, info, handler)
	if err != nil {
		t.Fatal(err)
	}
	if result != "ok" {
		t.Fatalf("result = %v", result)
	}
}

type authTestServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s authTestServerStream) Context() context.Context {
	return s.ctx
}

func TestStreamInterceptorRequiresSession(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth.json")
	state, _, err := InitializeAuth(
		statePath,
		"https://rs.example.com",
		"prod",
		"correct-horse-battery-staple",
	)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := LoadAuthManager(statePath, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{Auth: auth})
	interceptor := srv.StreamInterceptor()
	handler := func(any, grpc.ServerStream) error { return nil }

	if err := interceptor(nil, authTestServerStream{ctx: context.Background()}, nil, handler); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated stream error = %v", err)
	}

	session, err := auth.Login(state.AccountID, "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("authorization", "Bearer "+session.Token),
	)
	if err := interceptor(nil, authTestServerStream{ctx: ctx}, nil, handler); err != nil {
		t.Fatal(err)
	}
}
