terraform {
  required_providers {
    vcworkspace = {
      source  = "veritas-calculus/vcworkspace"
      version = "0.1.0"
    }
  }
}

provider "vcworkspace" {
  endpoint = "https://workspace.example.com"
  # api_token is read from VC_WORKSPACE_API_TOKEN.
}

data "vcworkspace_desktops" "available" {}

resource "vcworkspace_desktop_assignment" "engineering" {
  subject_type = "group"
  subject_id   = "engineering"
  desktop_vmid = 158
}
