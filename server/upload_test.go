package server

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "atlas/rs/gen/rs"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type uploadTestStream struct {
	ctx       context.Context
	requests  []*pb.UploadRequest
	recvBlock <-chan struct{}
	index     int
	ack       *pb.UploadAck
	mu        sync.Mutex
}

func (s *uploadTestStream) Recv() (*pb.UploadRequest, error) {
	if s.recvBlock != nil {
		select {
		case <-s.recvBlock:
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index >= len(s.requests) {
		return nil, io.EOF
	}
	request := s.requests[s.index]
	s.index++
	return request, nil
}

func (s *uploadTestStream) SendAndClose(ack *pb.UploadAck) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ack = ack
	return nil
}

func (s *uploadTestStream) SetHeader(metadata.MD) error  { return nil }
func (s *uploadTestStream) SendHeader(metadata.MD) error { return nil }
func (s *uploadTestStream) SetTrailer(metadata.MD)       {}
func (s *uploadTestStream) Context() context.Context     { return s.ctx }
func (s *uploadTestStream) SendMsg(any) error            { return nil }
func (s *uploadTestStream) RecvMsg(any) error            { return nil }

func newUploadTestStream(path string, data []byte, overwrite bool) *uploadTestStream {
	return &uploadTestStream{
		ctx: context.Background(),
		requests: []*pb.UploadRequest{
			{Payload: &pb.UploadRequest_Header{Header: &pb.UploadHeader{
				Path:      path,
				Size:      int64(len(data)),
				Overwrite: overwrite,
			}}},
			{Payload: &pb.UploadRequest_Data{Data: data}},
		},
	}
}

func TestUploadNoReplaceIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	server := New(Config{MaxUploadSize: 1 << 20})
	streams := []*uploadTestStream{
		newUploadTestStream(path, []byte("first"), false),
		newUploadTestStream(path, []byte("second"), false),
	}

	var group sync.WaitGroup
	for _, stream := range streams {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := server.Upload(stream); err != nil {
				t.Errorf("upload error = %v", err)
			}
		}()
	}
	group.Wait()

	successes := 0
	failures := 0
	for _, stream := range streams {
		switch {
		case stream.ack == nil:
			t.Fatal("upload returned no acknowledgement")
		case stream.ack.Error == "":
			successes++
		default:
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("successes=%d failures=%d", successes, failures)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" && string(data) != "second" {
		t.Fatalf("target data = %q", data)
	}
}

func TestUploadValidatesDeclaredSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	server := New(Config{MaxUploadSize: 1 << 20})
	stream := newUploadTestStream(path, []byte("unexpected"), false)
	stream.requests[0].GetHeader().Size = 0
	if err := server.Upload(stream); err != nil {
		t.Fatal(err)
	}
	if stream.ack == nil || stream.ack.Error == "" {
		t.Fatal("upload accepted data for a declared empty file")
	}

	stream = newUploadTestStream(path, nil, false)
	stream.requests[0].GetHeader().Size = -1
	if err := server.Upload(stream); err != nil {
		t.Fatal(err)
	}
	if stream.ack == nil || stream.ack.Error == "" {
		t.Fatal("upload accepted a negative declared size")
	}
}

func TestRecvUploadReturnsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stream := &uploadTestStream{ctx: ctx, recvBlock: make(chan struct{})}
	_, err := recvUpload(stream, time.Now().Add(time.Minute))
	if status.Code(err) != codes.Canceled {
		t.Fatalf("error = %v", err)
	}
}

func TestRecvUploadEnforcesIdleTimeout(t *testing.T) {
	previous := uploadIdleTimeout
	uploadIdleTimeout = 10 * time.Millisecond
	t.Cleanup(func() { uploadIdleTimeout = previous })

	block := make(chan struct{})
	stream := &uploadTestStream{
		ctx:       context.Background(),
		recvBlock: block,
	}
	_, err := recvUpload(stream, time.Now().Add(time.Minute))
	close(block)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("error = %v", err)
	}
}

func TestRecvUploadEnforcesMaximumDuration(t *testing.T) {
	stream := &uploadTestStream{
		ctx:       context.Background(),
		recvBlock: make(chan struct{}),
	}
	_, err := recvUpload(stream, time.Now().Add(-time.Second))
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("error = %v", err)
	}
}
