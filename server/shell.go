package server

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"

	pb "atlas/rs/gen/rs"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const defaultTimeout = 30 * time.Second

// shellWaitDelay bounds how long output pipes held open by background
// processes may delay the exit status after the shell itself has exited.
var shellWaitDelay = 5 * time.Second

// Shell executes script with sh -c and streams stdout/stderr to the client.
// The command runs in its own process group so the timeout kills the whole
// tree, not just the shell.
func (s *Server) Shell(req *pb.ShellRequest, stream grpc.ServerStreamingServer[pb.ShellOutput]) error {
	timeout := defaultTimeout
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}

	ctx, cancel := context.WithTimeout(stream.Context(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", req.Script)
	cmd.WaitDelay = shellWaitDelay
	configureProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd.Process)
	}
	// exec copies both pipes to completion before Wait returns, so no output
	// written before exit can be lost.
	var mu sync.Mutex
	cmd.Stdout = &shellWriter{mu: &mu, stream: stream, kind: pb.ShellOutput_STDOUT, cancel: cancel}
	cmd.Stderr = &shellWriter{mu: &mu, stream: stream, kind: pb.ShellOutput_STDERR, cancel: cancel}

	exitCode := int32(0)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		switch {
		case ctx.Err() == context.DeadlineExceeded:
			return status.Errorf(codes.DeadlineExceeded, "command timed out")
		case ctx.Err() != nil:
			// The context was cancelled by a failed send (client went away),
			// not a deadline; report it as a cancellation.
			return status.Errorf(codes.Canceled, "client disconnected")
		case errors.Is(err, exec.ErrWaitDelay):
			// The shell exited 0 but a background process kept its output
			// open; the shell's own status is what the caller asked for.
		case errors.As(err, &exitErr):
			exitCode = int32(exitErr.ExitCode())
		default:
			return status.Errorf(codes.Internal, "run: %v", err)
		}
	}

	return stream.Send(&pb.ShellOutput{Exited: true, ExitCode: exitCode})
}

// shellWriter forwards one output stream; stdout and stderr share mu because
// a gRPC stream allows only one concurrent sender.
type shellWriter struct {
	mu     *sync.Mutex
	stream grpc.ServerStreamingServer[pb.ShellOutput]
	kind   pb.ShellOutput_Stream
	cancel context.CancelFunc
}

func (w *shellWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	err := w.stream.Send(&pb.ShellOutput{Data: bytes.Clone(p), Stream: w.kind})
	w.mu.Unlock()
	if err != nil {
		w.cancel()
		return 0, err
	}
	return len(p), nil
}
