//go:build !windows

package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	pb "atlas/rs/gen/rs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type shellTestStream struct {
	ctx      context.Context
	delay    time.Duration
	sendErr  error
	messages []*pb.ShellOutput
	mu       sync.Mutex
}

func (s *shellTestStream) Send(message *pb.ShellOutput) error {
	if s.sendErr != nil && !message.Exited {
		return s.sendErr
	}
	time.Sleep(s.delay)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
	return nil
}

func (s *shellTestStream) SetHeader(metadata.MD) error  { return nil }
func (s *shellTestStream) SendHeader(metadata.MD) error { return nil }
func (s *shellTestStream) SetTrailer(metadata.MD)       {}
func (s *shellTestStream) Context() context.Context     { return s.ctx }
func (s *shellTestStream) SendMsg(any) error            { return nil }
func (s *shellTestStream) RecvMsg(any) error            { return nil }

func TestShellStreamsOutputAndExitCode(t *testing.T) {
	stream := &shellTestStream{ctx: context.Background()}
	server := New(Config{})
	if err := server.Shell(&pb.ShellRequest{
		Script:         "printf out; printf err >&2; exit 7",
		TimeoutSeconds: 5,
	}, stream); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exitCode, exited := collectShell(stream)
	if stdout != "out" || stderr != "err" || !exited || exitCode != 7 {
		t.Fatalf("stdout=%q stderr=%q exited=%v exit=%d", stdout, stderr, exited, exitCode)
	}
}

func TestShellEnforcesTimeout(t *testing.T) {
	stream := &shellTestStream{ctx: context.Background()}
	server := New(Config{})
	started := time.Now()
	err := server.Shell(&pb.ShellRequest{
		Script:         "sleep 10",
		TimeoutSeconds: 1,
	}, stream)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestShellDeliversAllOutputToSlowStream(t *testing.T) {
	const size = 2_000_000
	stream := &shellTestStream{ctx: context.Background(), delay: 2 * time.Millisecond}
	server := New(Config{})
	if err := server.Shell(&pb.ShellRequest{
		Script:         fmt.Sprintf("head -c %d /dev/zero; printf done >&2", size),
		TimeoutSeconds: 30,
	}, stream); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, exitCode, exited := collectShell(stream)
	if len(stdout) != size || stderr != "done" || !exited || exitCode != 0 {
		t.Fatalf("stdout=%d bytes stderr=%q exited=%v exit=%d", len(stdout), stderr, exited, exitCode)
	}
}

func TestShellBackgroundProcessDoesNotFailCommand(t *testing.T) {
	previous := shellWaitDelay
	shellWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { shellWaitDelay = previous })

	stream := &shellTestStream{ctx: context.Background()}
	server := New(Config{})
	if err := server.Shell(&pb.ShellRequest{
		Script:         "sleep 30 & printf started",
		TimeoutSeconds: 10,
	}, stream); err != nil {
		t.Fatal(err)
	}
	stdout, _, exitCode, exited := collectShell(stream)
	if stdout != "started" || !exited || exitCode != 0 {
		t.Fatalf("stdout=%q exited=%v exit=%d", stdout, exited, exitCode)
	}
}

func TestShellStopsWhenClientCannotReceive(t *testing.T) {
	stream := &shellTestStream{ctx: context.Background(), sendErr: errors.New("client gone")}
	server := New(Config{})
	started := time.Now()
	err := server.Shell(&pb.ShellRequest{
		Script:         "while :; do echo spam; done",
		TimeoutSeconds: 10,
	}, stream)
	if status.Code(err) != codes.Canceled {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func collectShell(stream *shellTestStream) (stdout, stderr string, exitCode int32, exited bool) {
	var out, errOut strings.Builder
	for _, message := range stream.messages {
		switch message.Stream {
		case pb.ShellOutput_STDOUT:
			out.Write(message.Data)
		case pb.ShellOutput_STDERR:
			errOut.Write(message.Data)
		}
		if message.Exited {
			exited = true
			exitCode = message.ExitCode
		}
	}
	return out.String(), errOut.String(), exitCode, exited
}
