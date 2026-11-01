package broker

import "context"

// Store persists broker data. Implementations must be safe for concurrent use,
// copy mutable inputs and outputs, and honor context cancellation. Creates fail
// with ErrConflict for duplicate IDs; gets, updates and deletes use ErrNotFound.
// Connection metadata and sealed credentials are written atomically. Store is
// trusted: it exposes ciphertext and client hashes, never plaintext credentials.
type Store interface {
	// CreateForge inserts a forge with a nonempty ID, kind and API base URL.
	CreateForge(context.Context, Forge) error
	// GetForge returns forge metadata.
	GetForge(context.Context, ID) (Forge, error)
	// ListForges returns all forges, ordered by ID.
	ListForges(context.Context) ([]Forge, error)
	// UpdateForge replaces metadata. Kind and BaseURL cannot change while
	// connections exist, to prevent sending credentials to another endpoint.
	UpdateForge(context.Context, Forge) error
	// DeleteForge fails with ErrConflict while any connection references it.
	DeleteForge(context.Context, ID) error

	// CreateConnection inserts metadata and nonempty ciphertext. ForgeID must exist.
	CreateConnection(context.Context, Connection, []byte) error
	// GetConnection returns metadata only.
	GetConnection(context.Context, ID) (Connection, error)
	// ListConnections returns metadata ordered by ID.
	ListConnections(context.Context) ([]Connection, error)
	// UpdateConnection atomically replaces metadata and, if non-nil, ciphertext.
	// ForgeID cannot change. A non-nil empty credential is invalid.
	UpdateConnection(context.Context, Connection, []byte) error
	// GetCredential returns a copy of the sealed credential, never plaintext.
	GetCredential(context.Context, ID) ([]byte, error)
	// DeleteConnection atomically deletes metadata and ciphertext, and removes
	// the ID from all client scopes.
	DeleteConnection(context.Context, ID) error

	// CreateClient inserts a client. All scoped connections must exist, and an
	// AllConnections client lists none.
	CreateClient(context.Context, Client) error
	// GetClient returns metadata and the token hash for trusted callers.
	GetClient(context.Context, ID) (Client, error)
	// ListClients returns clients with token hashes, ordered by ID.
	ListClients(context.Context) ([]Client, error)
	// UpdateClient replaces a client's metadata, scope and hash.
	UpdateClient(context.Context, Client) error
	// DeleteClient deletes a client, immediately invalidating its token.
	DeleteClient(context.Context, ID) error
}
