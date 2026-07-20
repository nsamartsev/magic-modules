# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Repo Does

Magic Modules is a code generator that produces the Terraform providers for Google Cloud (`google` GA and `google-beta`). Changes here are submitted as PRs; the `modular-magician` bot generates provider code, runs tests, and syncs to downstream provider repos.

## Key Commands

### Setup
```bash
./scripts/doctor   # Check dependencies (Go, git, Terraform, etc.)
```

### Generate Providers
Requires cloned provider repos at `$GOPATH/src/github.com/hashicorp/terraform-provider-google{,-beta}`.

```bash
# GA provider
make provider VERSION=ga OUTPUT_PATH="$GOPATH/src/github.com/hashicorp/terraform-provider-google"

# Beta provider
make provider VERSION=beta OUTPUT_PATH="$GOPATH/src/github.com/hashicorp/terraform-provider-google-beta"

# Single product only (skips clean-provider step — sync manually first)
make provider VERSION=ga OUTPUT_PATH="..." PRODUCT=compute

# Single resource
make provider VERSION=ga OUTPUT_PATH="..." PRODUCT=compute RESOURCE=Instance
```

### Unit Tests (within magic-modules)
```bash
make test                                      # All mmv1 unit tests
cd mmv1 && go test ./...                       # Same
cd mmv1 && go test ./api/... -run TestFoo -v   # Single test
```

### Acceptance Tests (in generated provider)
Run from the generated `terraform-provider-google` directory after generating:
```bash
make testacc TEST=./google/services/compute TESTARGS='-run=TestAccComputeInstance_basic$$'
```

## Architecture

### Two Code Paths

**MMv1** (`mmv1/`) — The primary generator. Resources are defined as YAML configs in `mmv1/products/{service}/`. The generator reads these configs, applies Go templates, and writes provider Go code.

**tpgtools** (`tpgtools/`) — A secondary generator for DCL (Declarative Client Library)-based resources. Uses OpenAPI schemas from `tpgtools/api/`. Less commonly used for new resources.

### MMv1 Structure

```
mmv1/
├── main.go                  # Entry point; parses flags, calls provider generators
├── api/                     # Core data models: Product, Resource, Type, Property
├── loader/                  # Reads and validates product YAML configs
├── provider/                # Generator implementations (terraform.go, tgc.go, oics.go)
├── products/                # ~170 product directories, each with:
│   └── {service}/
│       ├── product.yaml     # Product-level metadata (name, versions, base URLs)
│       └── ResourceName.yaml # Resource config (fields, CRUD methods, IAM, examples)
├── templates/terraform/     # Handwritten Go snippets injected into generated code
│   ├── custom_code/         # Full custom Go code blocks (pre/post CRUD operations)
│   └── examples/            # Terraform HCL examples embedded in tests/docs
└── third_party/terraform/   # Fully handwritten resources and data sources
    ├── services/            # Per-service handwritten .go files and _test.go files
    └── website/             # Handwritten documentation
```

### How Generation Works

1. `loader` reads `products/{service}/product.yaml` + `{Resource}.yaml` files into Go structs (`api.Product`, `api.Resource`, `api.Type`)
2. `provider/terraform.go` iterates resources and renders Go templates
3. Handwritten snippets from `templates/terraform/custom_code/` are injected at defined hook points
4. Fully handwritten resources in `third_party/terraform/services/` are copied verbatim
5. Output goes to `OUTPUT_PATH` (the downstream provider repo)

### Where to Make Changes

| Task | Location |
|---|---|
| New MMv1 resource | `mmv1/products/{service}/ResourceName.yaml` |
| New product | `mmv1/products/{service}/product.yaml` |
| Custom CRUD logic | `mmv1/templates/terraform/custom_code/` (referenced from resource YAML) |
| Handwritten resource/datasource | `mmv1/third_party/terraform/services/{service}/` |
| Handwritten docs | `mmv1/third_party/terraform/website/` |
| Resource tests | `mmv1/third_party/terraform/services/{service}/{resource}_test.go` |
| DCL-based resource | `tpgtools/api/{service}/`, `tpgtools/overrides/` |

### Versioning (GA vs Beta)

Resources/fields can be `ga`, `beta`, or both. In YAML configs:
- Fields with no `min_version` default to GA
- Fields with `min_version: beta` are beta-only
- The generator strips beta-only content when building the GA provider

### CI Bot (modular-magician)

The `.ci/magician/` directory contains the bot that runs on PRs. It generates both providers, runs tests against the generated code, and posts results back to GitHub. You generally don't need to run this locally.