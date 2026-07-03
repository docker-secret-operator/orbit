package compose

// Service is the Go representation of a single service stanza in a
// docker-compose.yml file. Only the fields Orbit cares about are
// decoded; all remaining fields are preserved verbatim in RawFields.
type Service struct {
	// Decoded fields — canonical Orbit inputs.
	Image       string            `yaml:"image,omitempty"`
	Ports       []string          `yaml:"ports,omitempty"`
	Environment map[string]string `yaml:"-"` // decoded via custom unmarshaller
	Networks    []string          `yaml:"-"` // decoded via custom unmarshaller
	DependsOn   []string          `yaml:"-"` // decoded via custom unmarshaller
	Expose      []string          `yaml:"expose,omitempty"`

	// XRollout holds Orbit-specific overrides. The field is stripped
	// from the generated file before it is written; Docker never sees it.
	XRollout XRolloutConfig `yaml:"x-docker-rollout,omitempty"`

	// RawFields preserves every field from the original YAML that is not
	// explicitly decoded above. These are written verbatim into the generated
	// file so that user-defined healthchecks, volumes, restart policies etc.
	// are never lost.
	RawFields map[string]interface{} `yaml:",inline"`
}

// XRolloutConfig holds the Orbit extension block for a service.
//
// Usage in docker-compose.yml:
//
//	x-docker-rollout:
//	  skip: true     # opt this service out of proxy injection
type XRolloutConfig struct {
	Skip bool `yaml:"skip,omitempty"`
}

// ComposeFile is the root structure of a parsed docker-compose.yml.
// The Services map is ordered — iteration order matches the original file.
type ComposeFile struct {
	Version  string                 `yaml:"version,omitempty"`
	Services map[string]Service     `yaml:"services"`
	Networks map[string]interface{} `yaml:"networks,omitempty"`
	Volumes  map[string]interface{} `yaml:"volumes,omitempty"`
}

// PortBinding links a host-side listen port to a service and target port.
type PortBinding struct {
	ListenPort int
	Service    string
	TargetPort int
}
