// docker-bake.hcl — declarative description of the multi-architecture image.
//
//   docker buildx bake -f docker-bake.hcl --print     # inspect the plan
//   docker buildx bake -f docker-bake.hcl --push      # build + push all platforms
//   REGISTRY=ghcr.io/me VERSION=0.2.0 docker buildx bake -f docker-bake.hcl --push
//
// `make docker-bake` wraps the last one with the Makefile's VERSION/COMMIT.
//
// The builder must be a docker-container builder (the default "docker" driver
// cannot produce a manifest list):
//   docker buildx create --name natpilot-multiarch --driver docker-container

variable "VERSION" { default = "0.1.0" }
variable "COMMIT" { default = "unknown" }
variable "BUILD_DATE" { default = "unknown" }
// Empty REGISTRY produces local tags (natpilot:...), which only make sense for a
// single platform; with several platforms you need a registry to push to.
variable "REGISTRY" { default = "" }
variable "PLATFORMS" {
  default = ["linux/amd64", "linux/arm64", "linux/arm/v7"]
}

group "default" {
  targets = ["image"]
}

target "image" {
  context    = "."
  dockerfile = "Dockerfile"
  platforms  = PLATFORMS

  args = {
    VERSION    = VERSION
    COMMIT     = COMMIT
    BUILD_DATE = BUILD_DATE
  }

  tags = REGISTRY == "" ? [
    "natpilot:${VERSION}",
    "natpilot:latest",
    ] : [
    "${REGISTRY}/natpilot:${VERSION}",
    "${REGISTRY}/natpilot:latest",
  ]

  // Provenance/SBOM attestations are useful when publishing; harmless locally.
  attest = [
    "type=provenance,mode=max",
    "type=sbom",
  ]
}
