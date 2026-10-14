package server

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	pb "atlas/rs/gen/rs"
	"atlas/rs/internal/fsx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const uploadChunkSize = 64 * 1024

var (
	uploadIdleTimeout = 2 * time.Minute
	uploadMaxDuration = 10 * time.Minute
)

// Upload receives a file as a stream (header then data chunks) and writes it
// atomically: data goes to a temp file in the target directory, which is
// committed only after the stream completes.
func (s *Server) Upload(stream grpc.ClientStreamingServer[pb.UploadRequest, pb.UploadAck]) error {
	deadline := time.Now().Add(uploadMaxDuration)
	msg, err := recvUpload(stream, deadline)
	if err != nil {
		if status.Code(err) == codes.DeadlineExceeded || status.Code(err) == codes.Canceled {
			return err
		}
		return s.uploadError(stream, fmt.Sprintf("recv header: %v", err))
	}
	hdr := msg.GetHeader()
	if hdr == nil {
		return s.uploadError(stream, "first message must be a header")
	}
	if !filepath.IsAbs(hdr.Path) {
		return s.uploadError(stream, "path must be absolute")
	}
	if hdr.Size < 0 {
		return s.uploadError(stream, "file size cannot be negative")
	}

	dir := filepath.Dir(hdr.Path)
	if hdr.Mkdir {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return s.uploadError(stream, fmt.Sprintf("mkdir: %v", err))
		}
	} else if _, err := os.Stat(dir); err != nil {
		return s.uploadError(stream, fmt.Sprintf("target directory does not exist: %s", dir))
	}
	if s.cfg.MaxUploadSize > 0 && hdr.Size > s.cfg.MaxUploadSize {
		return s.uploadError(stream, fmt.Sprintf("file size %d exceeds limit %d", hdr.Size, s.cfg.MaxUploadSize))
	}

	tmp, err := os.CreateTemp(dir, ".rs-uploading-*")
	if err != nil {
		return s.uploadError(stream, fmt.Sprintf("create temp file: %v", err))
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	defer tmp.Close()

	var written int64
	for {
		msg, err := recvUpload(stream, deadline)
		if err == io.EOF {
			break
		}
		if err != nil {
			if status.Code(err) == codes.DeadlineExceeded || status.Code(err) == codes.Canceled {
				return err
			}
			return s.uploadError(stream, fmt.Sprintf("recv: %v", err))
		}
		chunk := msg.GetData()
		if chunk == nil {
			continue
		}
		if len(chunk) > uploadChunkSize {
			return s.uploadError(stream, fmt.Sprintf("chunk size %d exceeds limit %d", len(chunk), uploadChunkSize))
		}
		written += int64(len(chunk))
		if s.cfg.MaxUploadSize > 0 && written > s.cfg.MaxUploadSize {
			return s.uploadError(stream, fmt.Sprintf("received %d bytes exceeds limit %d", written, s.cfg.MaxUploadSize))
		}
		if written > hdr.Size {
			return s.uploadError(stream, fmt.Sprintf("received %d bytes exceeds declared size %d", written, hdr.Size))
		}
		if _, err := tmp.Write(chunk); err != nil {
			return s.uploadError(stream, fmt.Sprintf("write: %v", err))
		}
	}
	if written != hdr.Size {
		return s.uploadError(stream, fmt.Sprintf("incomplete: received %d of declared %d bytes", written, hdr.Size))
	}

	if err := tmp.Chmod(0o644); err != nil {
		return s.uploadError(stream, fmt.Sprintf("chmod: %v", err))
	}
	if err := tmp.Sync(); err != nil {
		return s.uploadError(stream, fmt.Sprintf("sync: %v", err))
	}
	if err := tmp.Close(); err != nil {
		return s.uploadError(stream, fmt.Sprintf("close: %v", err))
	}
	if hdr.Overwrite {
		if err := os.Rename(tmpPath, hdr.Path); err != nil {
			return s.uploadError(stream, fmt.Sprintf("rename: %v", err))
		}
	} else {
		if err := os.Link(tmpPath, hdr.Path); err != nil {
			if os.IsExist(err) {
				return s.uploadError(stream, "file exists and overwrite not set")
			}
			return s.uploadError(stream, fmt.Sprintf("commit: %v", err))
		}
		if err := os.Remove(tmpPath); err != nil {
			return s.uploadError(stream, fmt.Sprintf("remove temp file: %v", err))
		}
	}
	if err := fsx.SyncDir(dir); err != nil {
		return s.uploadError(stream, fmt.Sprintf("sync directory: %v", err))
	}

	return stream.SendAndClose(&pb.UploadAck{BytesWritten: written})
}

type uploadReceiveResult struct {
	message *pb.UploadRequest
	err     error
}

func recvUpload(stream grpc.ClientStreamingServer[pb.UploadRequest, pb.UploadAck], deadline time.Time) (*pb.UploadRequest, error) {
	wait := min(uploadIdleTimeout, time.Until(deadline))
	if wait <= 0 {
		return nil, status.Error(codes.DeadlineExceeded, "upload duration exceeded")
	}
	result := make(chan uploadReceiveResult, 1)
	go func() {
		message, err := stream.Recv()
		result <- uploadReceiveResult{message: message, err: err}
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case received := <-result:
		return received.message, received.err
	case <-timer.C:
		return nil, status.Error(codes.DeadlineExceeded, "upload idle timeout")
	case <-stream.Context().Done():
		return nil, status.FromContextError(stream.Context().Err()).Err()
	}
}

func (s *Server) uploadError(stream grpc.ClientStreamingServer[pb.UploadRequest, pb.UploadAck], msg string) error {
	_ = stream.SendAndClose(&pb.UploadAck{Error: msg})
	return nil
}
