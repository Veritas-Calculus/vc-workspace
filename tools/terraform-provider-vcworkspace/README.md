# VC Workspace Terraform/OpenTofu Provider

This initial provider slice manages explicit desktop assignments and reads the managed desktop registry through the same audited control-plane API used by the Web console.

Create a time-bounded **IaC API credential** in **Access control → Accounts**, then export configuration without committing the secret:

```bash
export VC_WORKSPACE_ENDPOINT=https://workspace.example.com
export VC_WORKSPACE_API_TOKEN=vcwi_...
terraform plan
```

The provider is built with the Terraform Plugin Framework and is protocol-compatible with Terraform and OpenTofu. Existing assignments can be imported with `subject_type/URL-escaped-subject_id/desktop_vmid`. The API credential is shown once, expires after at most 90 days, and cannot create or revoke credentials.

This is a source-tree provider until the repository has a signed release and Registry publication. `make iac-check` runs its unit tests and vet checks. A local Terraform filesystem mirror can validate the example before publication:

```bash
cd tools/terraform-provider-vcworkspace
root="$(git rev-parse --show-toplevel)"
mirror="$root/.cache/terraform-provider-mirror"
work="$root/.cache/terraform-provider-smoke"
platform="$(go env GOOS)_$(go env GOARCH)"
destination="$mirror/registry.terraform.io/veritas-calculus/vcworkspace/0.1.0/$platform"
mkdir -p "$destination" "$work"
cp examples/main.tf "$work/main.tf"
go build -o "$destination/terraform-provider-vcworkspace_v0.1.0" .
TF_DATA_DIR="$work/data" terraform -chdir="$work" init -backend=false -plugin-dir="$mirror"
TF_DATA_DIR="$work/data" terraform -chdir="$work" validate
```

Do not treat a locally calculated provider checksum as a release lock file. Signed multi-platform packages and Registry-generated checksums are part of the release work.
