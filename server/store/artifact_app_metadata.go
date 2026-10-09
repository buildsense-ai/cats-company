package store

import "context"

// ArtifactAppMetadata is separate from the gateway's tunnel configuration.
// CreatorUID is immutable; zero means a legacy app with unknown provenance.
type ArtifactAppMetadata struct {
	AppID       string `json:"app_id"`
	AgentUID    int64  `json:"agent_uid"`
	CreatorUID  int64  `json:"creator_uid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	IconURL     string `json:"icon_url"`
}

type ArtifactAppMetadataStore interface {
	ListArtifactAppMetadata(context.Context, int64) (map[string]ArtifactAppMetadata, error)
	EnsureArtifactAppMetadata(context.Context, ArtifactAppMetadata) error
	UpdateArtifactAppMetadata(context.Context, ArtifactAppMetadata) error
	DeleteArtifactAppMetadata(context.Context, int64, string) error
}
