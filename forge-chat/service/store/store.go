// Package store persists chat presentation metadata.
package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"

	pb "github.com/r59q/forge-tools/forge-chat/service/chatpb"
	"google.golang.org/protobuf/encoding/protojson"
)

// ErrInvalid means the chat ID or display preferences are invalid.
var ErrInvalid = errors.New("invalid chat metadata")

// Store protects reads and updates to metadata within one process.
type Store struct {
	root *os.Root
	mu   sync.Mutex
}

// Open opens a dedicated directory for a single forge-chat instance.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create metadata directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open metadata directory: %w", err)
	}
	return &Store{root: root}, nil
}

// Close closes the metadata directory.
func (s *Store) Close() error {
	return s.root.Close()
}

// List returns all saved metadata records in chat ID order.
func (s *Store) List(ctx context.Context) ([]*pb.ChatMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := fs.ReadDir(s.root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("list metadata: %w", err)
	}
	var chats []*pb.ChatMetadata
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !ValidID(id) || !entry.Type().IsRegular() {
			continue
		}
		record, err := s.read(id)
		if err != nil {
			return nil, err
		}
		chats = append(chats, record)
	}
	return chats, nil
}

// Patch merges the supplied fields with the current metadata.
func (s *Store) Patch(ctx context.Context, req *pb.UpdateChatMetadataRequest) (*pb.ChatMetadata, error) {
	if !validPatch(req) {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	id := req.GetId()
	record, err := s.read(id)
	if err != nil {
		return nil, err
	}
	if req.Archived != nil {
		record.Archived = req.GetArchived()
	}
	if req.Links != nil {
		record.Links = &pb.ChatLinks{Ids: slices.Clone(req.GetLinks().GetIds())}
	}
	if req.IslandPosition != nil {
		position := req.GetIslandPosition()
		record.IslandPosition = &pb.IslandPosition{X: position.GetX(), Y: position.GetY(), Z: position.GetZ()}
	}
	return record, s.write(record)
}

func (s *Store) read(id string) (*pb.ChatMetadata, error) {
	data, err := s.root.ReadFile(id + ".json")
	if errors.Is(err, fs.ErrNotExist) {
		return &pb.ChatMetadata{Id: id}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	record := &pb.ChatMetadata{}
	if err := protojson.Unmarshal(data, record); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	if record.GetId() != id {
		return nil, errors.New("chat metadata ID mismatch")
	}
	return record, nil
}

func (s *Store) write(record *pb.ChatMetadata) error {
	data, err := protojson.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode metadata: %w", err)
	}
	// The store mutex serializes use of the temporary file. Only complete
	// records replace the previous version.
	name := "." + record.GetId() + ".tmp"
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		if removeErr := s.root.Remove(name); removeErr != nil {
			return fmt.Errorf("remove stale metadata write: %w", removeErr)
		}
		f, err = s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return fmt.Errorf("open metadata write: %w", err)
	}
	defer func() { _ = s.root.Remove(name) }()
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}
	if err := s.root.Rename(name, record.GetId()+".json"); err != nil {
		return fmt.Errorf("replace metadata: %w", err)
	}
	return nil
}
