// Package server implements authenticated remote shell and file upload.
package server

import (
	"context"
	"strings"

	pb "atlas/rs/gen/rs"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const authorizationMetadataKey = "authorization"

// Version is the server version reported by Health; the CLI mirrors it as
// its own version string so a single bump keeps both in sync.
const Version = "1.0.0"

// Config holds the runtime configuration of an rs server.
type Config struct {
	Auth          *AuthManager // password sessions; every privileged RPC requires one
	MaxUploadSize int64        // per-upload size limit in bytes (0 = unlimited)
}

// Server implements pb.RsServer.
type Server struct {
	pb.UnimplementedRsServer
	cfg Config
}

// New creates an rs server.
func New(cfg Config) *Server {
	return &Server{cfg: cfg}
}

// Health reports server liveness to authenticated clients.
func (s *Server) Health(_ context.Context, _ *pb.HealthRequest) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Version: Version}, nil
}

func bearerTokenFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if values := md.Get(authorizationMetadataKey); len(values) > 0 {
		scheme, token, ok := strings.Cut(values[0], " ")
		if ok && strings.EqualFold(scheme, "Bearer") {
			return token
		}
	}
	return ""
}

// authorize accepts a short-lived session issued by the HTTP login endpoint.
func (s *Server) authorize(ctx context.Context) error {
	if s.cfg.Auth != nil && s.cfg.Auth.VerifySession(bearerTokenFromContext(ctx)) == nil {
		return nil
	}
	return status.Error(codes.Unauthenticated, "invalid or expired rs session")
}

// UnaryInterceptor requires a valid short-lived session.
func (s *Server) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := s.authorize(ctx); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor requires a valid short-lived session.
func (s *Server) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := s.authorize(ss.Context()); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}
