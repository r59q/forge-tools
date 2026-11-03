package api

import (
	"context"
	"errors"
	"io"

	"github.com/r59q/forge-tools/forge-code/library/code"
	pb "github.com/r59q/forge-tools/forge-code/service/codepb"
	"github.com/r59q/forge-tools/forge-core/servicekit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// readAll reads a whole blob of at most MaxFileBytes and closes rd.
func readAll(rd *code.Reader) (*pb.File, error) {
	defer rd.Close()
	if rd.Size > MaxFileBytes {
		return nil, errFileTooLarge
	}

	data, err := io.ReadAll(io.LimitReader(rd, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, errFileTooLarge
	}

	b := blob(rd.Blob)
	b.Size = int64(len(data))

	return &pb.File{Blob: b, Content: data}, nil
}

// ReadFile streams a file.
func (s *Server) ReadFile(r *pb.ReadFileRequest, stream grpc.ServerStreamingServer[pb.ReadFileResponse]) error {
	chunk := func(b *pb.Blob, data []byte) error {
		return stream.Send(&pb.ReadFileResponse{Blob: b, Data: data})
	}

	return s.stream(stream.Context(), chunk, r.GetSource(), func(ctx context.Context, client *code.Client) (*code.Reader, error) {
		return client.ReadFile(ctx, repo(r), r.GetRevision(), r.GetPath())
	})
}

// ReadBlob streams a blob.
func (s *Server) ReadBlob(r *pb.ReadBlobRequest, stream grpc.ServerStreamingServer[pb.ReadBlobResponse]) error {
	chunk := func(b *pb.Blob, data []byte) error {
		return stream.Send(&pb.ReadBlobResponse{Blob: b, Data: data})
	}

	return s.stream(stream.Context(), chunk, r.GetSource(), func(ctx context.Context, client *code.Client) (*code.Reader, error) {
		return client.ReadBlob(ctx, repo(r), code.OID(r.GetId()))
	})
}

// sendChunk sends one stream message. Only the first carries the blob.
type sendChunk func(b *pb.Blob, data []byte) error

// opener opens the content a stream sends.
type opener func(context.Context, *code.Client) (*code.Reader, error)

// stream sends the content open returns in chunks of up to ChunkBytes,
// within the stream timeout. The first chunk carries the blob.
func (s *Server) stream(ctx context.Context, chunk sendChunk, source string, open opener) error {
	ctx, cancel := context.WithTimeout(ctx, s.streamTimeout)
	defer cancel()
	if err := s.authorize(ctx); err != nil {
		return err
	}

	client, release, err := s.begin(ctx, source)
	if err != nil {
		return err
	}

	// Send waits for a client that stops reading and ignores ctx, so the
	// copy runs apart. Returning ends the stream, which ends a pending
	// Send; the copy then frees the slot.
	done := make(chan error, 1)
	go func() {
		defer release()
		done <- send(ctx, chunk, client, open)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return rpcError(ctx.Err())
	}
}

// send streams the content open returns.
func send(ctx context.Context, chunk sendChunk, client *code.Client, open opener) error {
	rd, err := open(ctx, client)
	if err != nil {
		return rpcError(err)
	}

	defer rd.Close()
	buf := make([]byte, ChunkBytes)
	first := blob(rd.Blob)
	var total int64
	for {
		n, err := fill(rd, buf)
		eof := errors.Is(err, io.EOF)
		if err != nil && !eof {
			return rpcError(err)
		}

		total += int64(n)
		if eof && rd.Size >= 0 && total != rd.Size {
			return servicekit.Status(codes.Unavailable)
		}
		if n > 0 || first != nil {
			if err := chunk(first, buf[:n]); err != nil {
				return err
			}

			first = nil
		}

		if eof {
			return nil
		}
	}
}

// fill reads into buf until it is full or r fails. It returns io.EOF only
// at the end of r, unlike io.ReadFull, which also turns an
// io.ErrUnexpectedEOF from r into a normal short read.
func fill(r io.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}

	return n, nil
}
