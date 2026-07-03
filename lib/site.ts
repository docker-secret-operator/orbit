const repo = "https://github.com/docker-secret-operator/orbit";

export const site = {
  name: "Orbit",
  domain: "https://orbit.deploy", // placeholder canonical origin
  tagline: "Zero-downtime deployments for Docker Compose.",
  description:
    "Orbit is a single Go binary that adds zero-downtime, health-aware rolling deploys to your existing Docker Compose stack — no Kubernetes, no Traefik, no proxy to run yourself.",
  repo,
  docs: `${repo}/tree/main/docs`,
  releases: `${repo}/releases`,
  license: `${repo}/blob/main/LICENSE`,
  examples: `${repo}/tree/main/examples`,
  installScript:
    "curl -fsSL https://raw.githubusercontent.com/docker-secret-operator/orbit/main/install.sh | bash",
  // Deep links to real source documents, so claims are verifiable.
  links: {
    reliabilityReport: `${repo}/blob/main/docs/reliability-report.md`,
    constitution: `${repo}/blob/main/CONSTITUTION.md`,
    changelog: `${repo}/blob/main/CHANGELOG.md`,
    deploymentGuide: `${repo}/blob/main/docs/deployment-guide.md`,
    troubleshooting: `${repo}/blob/main/docs/troubleshooting.md`,
    cliReference: `${repo}/tree/main/docs/cli-reference`,
    installation: `${repo}/blob/main/docs/installation.md`,
  },
  // Factual project status — no invented numbers, no adoption metrics.
  status: {
    maturity: "Release candidate", // self-assessed in docs/reliability-report.md
    versioning: "Pre-1.0 · SemVer once tagged",
    license: "MIT",
    language: "Go 1.26",
    platforms: "Linux · macOS",
    arch: "amd64 · arm64",
  },
} as const;
