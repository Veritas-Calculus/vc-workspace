# Debian xrdp display reactivation

Runtime package for the Debian 13 cursor-cache failure reproduced through the
formal Mac client and infra Gateway. Template rollout and acceptance status are maintained only in
[MAC-03 / NET-01](../../../../docs/plan/status.md).

Debian `xrdp 0.10.1-3.1+deb13u2` resets its pointer cache during display resize
but retains the screen's old cache index. A subsequent mouse move may reference
an image the reactivated client has not received. The patch snapshots the
current image, defers cached references during reset, retains newer backend
cursor changes, and resends the image after the static cursors are initialized.
FreeRDP cache recreation and invalid-update rejection remain enabled.

Build the Debian package, run its source tests, then exercise the actual
fresh-image installer in another isolated amd64 container:

```bash
make debian-xrdp-check \
  XRDP_DEBIAN_MIRROR=http://10.31.0.2/debian \
  XRDP_DEBIAN_SECURITY_MIRROR=http://10.31.0.2/debian-security \
  XRDP_OUTPUT=dist/debian-13-xrdp
```

APT verifies the signed Debian repository metadata. The exact upstream and
Debian security-patch archives are additionally SHA-256 pinned. No unsigned
repository option, host package installation, or PVE access is used by this
build. Without `XRDP_OUTPUT`, artifacts go to `.cache/xrdp-resize-build/` for
the opt-in VM160 lab. The container installation verifies rejection of a wrong
digest without changing the installed version, real apt installation and dpkg
checksums, and rejection of a repeated fresh-image installation. Its receipt
is exported under `verification/`; this is not a PVE/OS desktop acceptance.

The build emits `xrdp.deb`, canonical `xrdp-package.sha256`, the versioned Debian
package/buildinfo, and an extracted daemon for the isolated lab. The sidecar
detects changed bytes; it does not authenticate a publisher. Obtain both files
from the reviewed source build, not from an untrusted download.

For Web Bootstrap, configure `VC_WORKSPACE_LINUX_XRDP_BUNDLE` on the builder
(default `dist/debian-13-xrdp`). The controller verifies the package on startup,
pins its digest, and rechecks before launching a Debian build. Replacing both
files while the builder is running is rejected; review the new artifact and
restart the idle builder to adopt it. Package paths and digests are not web API
parameters. Direct Packer users must supply `xrdp_package` and the independent
`xrdp_package_sha256` variable.

The Debian recipe now requires this package. After upload, the root installer
validates the digest, stages the verified bytes privately, and checks package
name, amd64 architecture and exact security/local version before apt runs. It
accepts only the expected stock Debian base in a fresh image, not an occupied
desktop, unknown newer version or already provisioned runtime. The installed
package is verified against dpkg checksums before creating the root-owned
`/usr/share/vc-workspace/rdp-runtime.json` receipt (package version/digest,
daemon digest and fix identifier).

Existing PVE templates and running desktops are not changed by this build.
Rebase and retest when the Debian security version changes; the installer fails
instead of downgrading. No apt hold or security-update suppression is installed.

For the existing, explicitly opted-in VM160 lab, setting
`VC_WORKSPACE_LAB_PATCHED_XRDP=true` on `tools/test-infra-gateway.mjs install-agent`
also installs the built daemon. This requires a fresh idle lab and an exact
known original package/binary hash. The fixture backs up the original file,
stops the idle listener, verifies the amd64 payload, and starts the listener.
Normal lab cleanup restores and verifies the original daemon and its running
listener, alongside the Agent/PAM files. It does not install the whole `.deb`,
change PAM configuration through package maintainer scripts, or upgrade Guest
dependencies. An uncertain QGA result must be polled, never blindly retried.
