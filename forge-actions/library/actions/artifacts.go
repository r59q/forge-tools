package actions

import (
	"context"
	"encoding/json"
	"time"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

// ListArtifacts lists pipeline archives, up to 1000 entries. GitLab returns
// each latest job's archive; individual report files are not downloadable
// through this API. An expired or deleted artifact may no longer be listed.
func (c *Client) ListArtifacts(ctx context.Context, r Ref) ([]Artifact, error) {
	s, base, err := c.resolveID(r)
	if err != nil {
		return nil, err
	}

	path, envelope := base+"/actions/runs/"+r.ID+"/artifacts", "artifacts"
	if s.Session.Kind == forgeauth.Forgejo {
		envelope = ""
	}
	if s.Session.Kind == forgeauth.GitLab {
		path, envelope = base+"/pipelines/"+r.ID+"/jobs", ""
	}

	items, err := readAll(ctx, s, path, nil, envelope)
	if err != nil {
		return nil, err
	}

	out := make([]Artifact, 0, len(items))
	for _, raw := range items {
		artifact, include, err := decodeArtifact(s.Session.Kind, r, raw)
		if err != nil {
			return nil, err
		}
		if include {
			out = append(out, artifact)
		}
	}

	return out, nil
}

// GitLab job entries without archives are omitted from the artifact listing.
func decodeArtifact(kind forgeauth.Kind, r Ref, raw json.RawMessage) (Artifact, bool, error) {

	var v struct {
		ID                int64
		Name              string
		Size              *int64 `json:"size_in_bytes"`
		Expired           *bool
		CreatedAt         time.Time `json:"created_at"`
		ExpiresAt         time.Time `json:"expires_at"`
		ArtifactsExpireAt time.Time `json:"artifacts_expire_at"`
		File              struct {
			Filename string
			Size     *int64
		} `json:"artifacts_file"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return Artifact{}, false, err
	}

	a := Artifact{
		Ref:          Ref{RepositoryRef: r.RepositoryRef, ID: nativeID(v.ID)},
		PipelineID:   r.ID,
		Name:         v.Name,
		Size:         -1,
		Availability: "unknown",
		CreatedAt:    v.CreatedAt,
		ExpiresAt:    v.ExpiresAt,
	}
	if a.ID == "" {
		return Artifact{}, false, ErrUnsupported
	}
	if v.Size != nil {
		a.Size = *v.Size
	}
	if v.Expired != nil {
		a.Availability = "available"
		if *v.Expired {
			a.Availability = "expired"
		}
	}

	if kind == forgeauth.GitLab {
		if v.File.Filename == "" {
			return Artifact{}, false, nil
		}

		a.JobID, a.Name, a.ExpiresAt = a.ID, v.File.Filename, v.ArtifactsExpireAt
		a.Availability = "available"
		if v.File.Size != nil {
			a.Size = *v.File.Size
		}
	}

	return a, true, nil
}

// DownloadArtifact returns the archive bytes, up to MaxArtifactBytes. It
// never extracts an archive. Missing and expired archives retain forge errors.
// Use the native forge link for archives larger than the limit.
func (c *Client) DownloadArtifact(ctx context.Context, r Ref) ([]byte, error) {
	s, base, err := c.resolveID(r)
	if err != nil {
		return nil, err
	}

	path := base + "/actions/artifacts/" + r.ID + "/zip"
	if s.Session.Kind == forgeauth.GitLab {
		path = base + "/jobs/" + r.ID + "/artifacts"
	}

	data, over, err := readDownload(ctx, s, path, MaxArtifactBytes)
	if err != nil {
		return nil, err
	}
	if over {
		return nil, ErrTooLarge
	}

	return data, nil
}
