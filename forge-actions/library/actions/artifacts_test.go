package actions

import (
	"testing"

	"github.com/r59q/forge-tools/forge-core/forgeapi/forgeauth"
)

func TestArtifactAvailability(t *testing.T) {
	repository := RepositoryRef{Source: "source", Repository: "owner/repo"}
	pipeline := Ref{RepositoryRef: repository, ID: "7"}

	for _, tc := range []struct {
		name         string
		kind         forgeauth.Kind
		payload      string
		availability string
		include      bool
	}{
		{"unknown metadata", forgeauth.GitHub, `{"id":11,"name":"archive"}`, "unknown", true},
		{"expired archive", forgeauth.Forgejo, `{"id":11,"expired":true}`, "expired", true},
		{"job without archive", forgeauth.GitLab, `{"id":11,"name":"test"}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			artifact, include, err := decodeArtifact(tc.kind, pipeline, []byte(tc.payload))
			if err != nil {
				t.Fatal(err)
			}
			if include != tc.include || artifact.Availability != tc.availability {
				t.Fatalf("unexpected availability: %+v, included: %v", artifact, include)
			}
			if include && (artifact.Size != -1 || artifact.PipelineID != pipeline.ID) {
				t.Fatalf("missing size or pipeline identity: %+v", artifact)
			}
		})
	}
}
