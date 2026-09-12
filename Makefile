.PHONY: dev server web macos-app macos-check check test iac-check images-check images-live-check identity-lab-check k8s-check container-build pve-check pve-live-gpu-check pve-live-desktop-resolution-check pve-live-desktop-dpi-check pve-live-linux-privilege-check pve-live-linux-session-policy-check pve-live-linux-session-policy-verify pve-live-windows-privilege-check pve-live-native-identity-check pve-live-ad-identity-check pve-live-computer-check pve-live-mcp-computer-check

GOCACHE ?= $(CURDIR)/.cache/go-build
export GOCACHE

dev:
	@echo "Run 'make server' and 'make web' in separate terminals."

server:
	go run ./apps/control-plane

web:
	pnpm --filter @vc-workspace/web dev

macos-app:
	cd clients/macos && ./scripts/build-app.sh release

.PHONY: session-worker-check
session-worker-check:
	cmake -S apps/session-worker -B .cache/session-worker -DCMAKE_BUILD_TYPE=Release
	cmake --build .cache/session-worker -j 4
	ctest --test-dir .cache/session-worker --output-on-failure
	go test -race ./internal/rdpsession

SESSION_WORKER_IMAGE ?= vc-workspace-session-worker:local
SESSION_WORKER_DEBIAN_SECURITY_MIRROR ?= http://deb.debian.org/debian-security
.PHONY: session-worker-container-check
session-worker-container-check:
	docker build --build-arg DEBIAN_MIRROR="$(COMPUTER_TEST_DEBIAN_MIRROR)" --build-arg DEBIAN_SECURITY_MIRROR="$(SESSION_WORKER_DEBIAN_SECURITY_MIRROR)" -f deploy/container/session-worker.Dockerfile -t "$(SESSION_WORKER_IMAGE)" .
	bash apps/session-worker/test-container.sh "$(SESSION_WORKER_IMAGE)"

macos-check:
	bash -n clients/macos/scripts/build-app.sh
	bash -n clients/macos/scripts/build-native-rdp.sh
	bash -n clients/macos/scripts/verify-app.sh
	plutil -lint clients/macos/App/Info.plist
	cd clients/macos && SWIFTPM_MODULECACHE_OVERRIDE=$(CURDIR)/.cache/swift CLANG_MODULE_CACHE_PATH=$(CURDIR)/.cache/swift swift test --disable-sandbox
	cd clients/macos && ./scripts/build-app.sh release
	clients/macos/scripts/verify-app.sh
	$(MAKE) macos-gateway-check
	$(MAKE) macos-rdp-reactivation-check

.PHONY: macos-gateway-check
macos-gateway-check:
	@test "$$(uname -s)" = Darwin || (echo "macos-gateway-check requires macOS"; exit 1)
	@test -f 'clients/macos/.build/app/VC Workspace.app/Contents/Frameworks/libVCWorkspaceRDP.dylib' || (echo "Build the current macOS app first with make macos-app"; exit 1)
	go test -race -c ./internal/gateway -o clients/macos/.build/gateway-interop-fixture
	cd clients/macos && VC_WORKSPACE_TEST_GATEWAY_FIXTURE="$$PWD/.build/gateway-interop-fixture" VC_WORKSPACE_RDP_RUNTIME_PATH="$$PWD/.build/app/VC Workspace.app/Contents/Frameworks/libVCWorkspaceRDP.dylib" swift test --disable-sandbox

.PHONY: macos-rdp-reactivation-check
macos-rdp-reactivation-check:
	@test "$$(uname -s)" = Darwin || (echo "macos-rdp-reactivation-check requires macOS"; exit 1)
	bash clients/macos/scripts/test-rdp-reactivation.sh

.PHONY: macos-gateway-soak-check
macos-gateway-soak-check:
	@test "$$(uname -s)" = Darwin || (echo "macos-gateway-soak-check requires macOS"; exit 1)
	go test -race -c ./internal/gateway -o clients/macos/.build/gateway-interop-fixture
	cd clients/macos && VC_WORKSPACE_TEST_GATEWAY_FIXTURE="$$PWD/.build/gateway-interop-fixture" VC_WORKSPACE_TEST_GATEWAY_SOAK=true swift test --disable-sandbox --filter sustainedBurstsAndLongIdleKeepTheSameAuthorizedTunnel

check: pve-check macos-check iac-check
	go test ./...
	go vet ./...
	cargo test --workspace
	pnpm check
	pnpm test
	pnpm build

test:
	go test ./...
	pnpm test

.PHONY: gateway-check
gateway-check:
	@test -n "$$VC_WORKSPACE_TEST_DATABASE_URL" || (echo "Set VC_WORKSPACE_TEST_DATABASE_URL to a disposable PostgreSQL database"; exit 1)
	go test -race ./internal/store ./internal/gateway ./internal/httpapi ./internal/guestdesktop -run 'Test(NativeGateway|Relay|Control|WebSocket|Gateway|BrokerRoute|RDPCertificate)' -count=2 -timeout=3m

.PHONY: pve-live-gateway-certificate-check
pve-live-gateway-certificate-check:
	@test "$$VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID" = 160
	@test -n "$$VC_WORKSPACE_LIVE_PVE_ENDPOINT"
	@test -n "$$VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE"
	VC_WORKSPACE_LIVE_GATEWAY_CERTIFICATE_AUDIT=true go test ./internal/httpapi -run '^TestLiveGatewayGuestCertificate$$' -v -count=1 -timeout=1m

iac-check:
	cd tools/terraform-provider-vcworkspace && go test ./...
	cd tools/terraform-provider-vcworkspace && go vet ./...

images-check:
	find deploy/images -type f -name '*.sh' -print0 | xargs -0 -n1 bash -n
	python3 deploy/images/debian-13-xfce/xrdp/test-install-package.py
	packer fmt -check -recursive deploy/images
	packer validate -syntax-only deploy/images/debian-13-xfce/debian-13-xfce.pkr.hcl
	packer validate -syntax-only deploy/images/windows-client/windows-client.pkr.hcl

.PHONY: images-packer-check
# Requires packer init for both recipes; validates real plugin configuration,
# with non-secret fixtures and no PVE access or VM creation.
images-packer-check:
	VC_WORKSPACE_TEST_PACKER_VALIDATE=true go test ./internal/imagebuilder -run '^TestPackerTemplateConfigValidation$$' -v -count=1 -timeout=3m

.PHONY: debian-xrdp-check debian-xrdp-install-check
XRDP_DEBIAN_MIRROR ?= http://deb.debian.org/debian
XRDP_DEBIAN_SECURITY_MIRROR ?= http://deb.debian.org/debian-security
XRDP_OUTPUT ?= .cache/xrdp-resize-build
debian-xrdp-check:
	docker buildx build --platform linux/amd64 --build-arg DEBIAN_MIRROR="$(XRDP_DEBIAN_MIRROR)" --build-arg DEBIAN_SECURITY_MIRROR="$(XRDP_DEBIAN_SECURITY_MIRROR)" --output "type=local,dest=$(XRDP_OUTPUT)" deploy/images/debian-13-xfce/xrdp
	$(MAKE) debian-xrdp-install-check

debian-xrdp-install-check:
	docker buildx build --platform linux/amd64 --build-context "xrdp_bundle=$(XRDP_OUTPUT)" --build-arg DEBIAN_MIRROR="$(XRDP_DEBIAN_MIRROR)" --build-arg DEBIAN_SECURITY_MIRROR="$(XRDP_DEBIAN_SECURITY_MIRROR)" --file deploy/images/debian-13-xfce/xrdp/install-test.Dockerfile --output "type=local,dest=$(XRDP_OUTPUT)/verification" deploy/images/debian-13-xfce/xrdp

images-live-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	VC_WORKSPACE_LIVE_TEMPLATE_AUDIT=true go test ./internal/pve -run TestLiveMaintainedDesktopTemplates -v

identity-lab-check:
	deploy/identity-lab/check.sh

COMPUTER_TEST_RUST_IMAGE ?= rust:1.88-bookworm
COMPUTER_TEST_DEBIAN_MIRROR ?= http://deb.debian.org/debian
.PHONY: computer-session-check
computer-session-check:
	docker build --build-arg RUST_IMAGE="$(COMPUTER_TEST_RUST_IMAGE)" --build-arg DEBIAN_MIRROR="$(COMPUTER_TEST_DEBIAN_MIRROR)" --target test -f deploy/container/guest-agent-test.Dockerfile -t vc-workspace-computer-session:test .
	docker run --init --rm --network none --env VC_WORKSPACE_DISPOSABLE_TEST=1 vc-workspace-computer-session:test python3 /workspace/tools/test-linux-account-lease.py
	docker run --init --rm --network none --env VC_WORKSPACE_DISPOSABLE_TEST=1 vc-workspace-computer-session:test python3 /workspace/tools/test-linux-native-account.py
	docker run --init --rm --network none --env VC_WORKSPACE_DISPOSABLE_TEST=1 vc-workspace-computer-session:test python3 /workspace/tools/test-linux-native-pam.py
	docker run --init --rm --network none --env VC_WORKSPACE_DISPOSABLE_TEST=1 vc-workspace-computer-session:test python3 /workspace/tools/test-linux-display-cleanup.py
	docker run --init --rm --network none --env VC_WORKSPACE_DISPOSABLE_TEST=1 vc-workspace-computer-session:test

WINDOWS_SESSION_TEST_DEBIAN_IMAGE ?= debian:trixie-slim
WINDOWS_SESSION_TEST_DEBIAN_SECURITY_MIRROR ?= http://deb.debian.org/debian-security
.PHONY: windows-session-client-check
windows-session-client-check:
	docker build --build-arg DEBIAN_IMAGE="$(WINDOWS_SESSION_TEST_DEBIAN_IMAGE)" --build-arg DEBIAN_MIRROR="$(COMPUTER_TEST_DEBIAN_MIRROR)" --build-arg DEBIAN_SECURITY_MIRROR="$(WINDOWS_SESSION_TEST_DEBIAN_SECURITY_MIRROR)" -f deploy/container/windows-session-test.Dockerfile -t vc-workspace-windows-session-test:local .
	node tools/test-windows-session-client.mjs

k8s-check:
	bash -n deploy/kubernetes/verify.sh
	deploy/kubernetes/verify.sh

container-build:
	docker build -f deploy/container/control-plane.Dockerfile -t vc-workspace-control-plane:dev .
	docker build -f deploy/container/mcp.Dockerfile -t vc-workspace-mcp:dev .
	docker build -f deploy/container/web.Dockerfile -t vc-workspace-web:dev .

pve-check:
	find deploy/pve -type f -name '*.sh' -print0 | xargs -0 -n1 bash -n
	bash deploy/pve/test-gpu-passthrough-tools.sh

pve-live-gpu-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	VC_WORKSPACE_LIVE_GPU_AUDIT=true go test ./internal/pve -run TestLiveGPUInventory -v

.PHONY: pve-live-guest-service-check
pve-live-guest-service-check:
	test -n "$(VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID)"
	VC_WORKSPACE_LIVE_GUEST_SERVICE_AUDIT=true go test ./internal/httpapi -run '^TestLiveLinuxGuestServices$$' -v -count=1 -timeout=10m

.PHONY: pve-live-native-desktop-check
pve-live-native-desktop-check:
	test "$(VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID)" = 160
	test -n "$(VC_WORKSPACE_LIVE_NATIVE_DESKTOP_ADDRESS)"
	VC_WORKSPACE_LIVE_NATIVE_DESKTOP_AUDIT=true go test ./internal/httpapi -run '^TestLiveLinuxNativeDesktopRetention$$' -v -count=1 -timeout=6m

.PHONY: pve-live-guest-cold-boot-check
pve-live-guest-cold-boot-check:
	test -n "$(VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID)"
	VC_WORKSPACE_LIVE_GUEST_COLD_BOOT_AUDIT=true go test ./internal/httpapi -run '^TestLiveLinuxGuestColdBootExpiry$$' -v -count=1 -timeout=8m

.PHONY: pve-live-login-tree-check
pve-live-login-tree-check:
	test -n "$(VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID)"
	VC_WORKSPACE_LIVE_LOGIN_TREE_AUDIT=true go test ./internal/httpapi -run '^TestLiveLinuxLoginProcessTree$$' -v -count=1 -timeout=4m

.PHONY: pve-live-login-receipt-check
pve-live-login-receipt-check:
	test -n "$(VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID)"
	VC_WORKSPACE_LIVE_LOGIN_RECEIPT_AUDIT=true go test ./internal/httpapi -run '^TestLiveLinuxLoginCreationReceipt$$' -v -count=1 -timeout=5m

.PHONY: pve-live-login-queue-check
pve-live-login-queue-check:
	test -n "$(VC_WORKSPACE_LIVE_GUEST_SERVICE_VMID)"
	VC_WORKSPACE_LIVE_LOGIN_QUEUE_AUDIT=true go test ./internal/httpapi -run '^TestLiveLinuxQueuedLoginAndLogindRecovery$$' -v -count=1 -timeout=5m

.PHONY: pve-live-desktop-renderer-check
pve-live-desktop-renderer-check:
	VC_WORKSPACE_LIVE_DESKTOP_RENDERER_AUDIT=true VC_WORKSPACE_LIVE_PVE_MUTATIONS_ENABLED=true go test ./internal/pve -run '^TestLiveGuestDesktopRenderer$$' -v -count=1

pve-live-desktop-resolution-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_WORKSPACE_LIVE_DESKTOP_VMID)"
	VC_WORKSPACE_LIVE_DESKTOP_RESOLUTION_AUDIT=true VC_WORKSPACE_LIVE_PVE_MUTATIONS_ENABLED=true go test ./internal/pve -run TestLiveGuestDesktopResolution -v -count=1

pve-live-desktop-dpi-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_WORKSPACE_LIVE_DESKTOP_VMID)"
	test -n "$(VC_WORKSPACE_LIVE_DESKTOP_DPI)"
	VC_WORKSPACE_LIVE_DESKTOP_DPI_APPLY=true VC_WORKSPACE_LIVE_PVE_MUTATIONS_ENABLED=true go test ./internal/pve -run TestLiveGuestDesktopDPI -v -count=1

pve-live-linux-privilege-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_WORKSPACE_LIVE_PRIVILEGE_VMID)"
	VC_WORKSPACE_LIVE_PRIVILEGE_AUDIT=true go test ./internal/httpapi -run TestLiveLinuxDesktopPrivilegePolicy -v

pve-live-linux-session-policy-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_WORKSPACE_LIVE_SESSION_POLICY_VMID)"
	VC_WORKSPACE_LIVE_SESSION_POLICY_AUDIT=true go test ./internal/httpapi -run TestLiveLinuxDesktopSessionPolicy -v -count=1

pve-live-linux-session-policy-verify:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_WORKSPACE_LIVE_SESSION_POLICY_VMID)"
	VC_WORKSPACE_LIVE_SESSION_POLICY_VERIFY=true go test ./internal/httpapi -run TestLiveLinuxDesktopManagedPolicyState -v -count=1

pve-live-windows-privilege-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_WORKSPACE_LIVE_PRIVILEGE_VMID)"
	VC_WORKSPACE_LIVE_WINDOWS_PRIVILEGE_AUDIT=true go test ./internal/httpapi -run TestLiveWindowsDesktopPrivilegePolicy -v

pve-live-native-identity-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	@test -n "$$VC_WORKSPACE_LIVE_DATABASE_URL"
	test -n "$(VC_WORKSPACE_LIVE_NATIVE_IDENTITY_VMID)"
	VC_WORKSPACE_LIVE_NATIVE_IDENTITY_AUDIT=true go test ./internal/httpapi -run TestLiveNativePerUserIdentityLifecycle -v -timeout 10m

.PHONY: pve-live-guest-revocation-check
pve-live-guest-revocation-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	@test -n "$$VC_WORKSPACE_TEST_DATABASE_URL"
	test -n "$(VC_WORKSPACE_LIVE_GUEST_REVOCATION_VMID)"
	VC_WORKSPACE_LIVE_GUEST_REVOCATION=true go test ./internal/httpapi -run '^TestLiveLinuxRetainedGuestRevocation$$' -v -count=1 -timeout 4m

pve-live-ad-identity-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_WORKSPACE_LIVE_AD_SERVER_VMID)"
	test -n "$(VC_WORKSPACE_LIVE_AD_CLIENT_VMID)"
	test -n "$(VC_WORKSPACE_LIVE_AD_DOMAIN)"
	test -n "$(VC_WORKSPACE_LIVE_AD_REALM)"
	test -n "$(VC_WORKSPACE_LIVE_AD_ALLOWED_GROUP)"
	test -n "$(VC_WORKSPACE_LIVE_AD_JOIN_USERNAME)"
	@test -n "$$VC_WORKSPACE_LIVE_AD_JOIN_PASSWORD"
	@test -n "$$VC_WORKSPACE_LIVE_AD_ALICE_PASSWORD"
	@test -n "$$VC_WORKSPACE_LIVE_AD_BOB_PASSWORD"
	VC_WORKSPACE_LIVE_AD_IDENTITY_AUDIT=true go test ./internal/httpapi -run TestLiveADIdentityReconcile -v -count=1 -timeout 10m

pve-live-computer-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_WORKSPACE_LIVE_COMPUTER_VMID)"
	VC_WORKSPACE_LIVE_COMPUTER_AUDIT=true go test ./internal/computer -run TestLiveComputerUseGuest -v -timeout 15m

pve-live-mcp-computer-check:
	test -n "$(VC_WORKSPACE_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_WORKSPACE_LIVE_COMPUTER_VMID)"
	test -n "$(VC_WORKSPACE_LIVE_COMPUTER_EPOCH_FLOOR)"
	@test -n "$$VC_WORKSPACE_TEST_DATABASE_URL"
	VC_WORKSPACE_LIVE_MCP_COMPUTER_AUDIT=true go test ./apps/mcp -run '^TestLiveMCPComputerUse$$' -v -count=1 -timeout 10m

.PHONY: pve-live-windows-session-check
pve-live-windows-session-check:
	@test -n "$$VC_WORKSPACE_LIVE_PVE_ENDPOINT"
	@test -n "$$VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE"
	@test -n "$$VC_WORKSPACE_LIVE_COMPUTER_VMID"
	@test -n "$$VC_WORKSPACE_LIVE_WINDOWS_SESSION_TEST_BINARY"
	VC_WORKSPACE_LIVE_WINDOWS_SESSION_PRIMITIVES=true go test ./internal/computer -run '^TestLiveWindowsSessionPrimitives$$' -v -count=1 -timeout 15m

.PHONY: pve-live-windows-accounts-check
pve-live-windows-accounts-check:
	VC_WORKSPACE_LIVE_WINDOWS_ACCOUNT_LIFECYCLE=true $(MAKE) pve-live-windows-session-check

# Explicit destructive fresh-Profile opt-in. Includes unresolved DPAPI
# preservation regressions; see status.md before interpreting a failed run.
.PHONY: pve-live-windows-profile-check
pve-live-windows-profile-check:
	VC_WORKSPACE_LIVE_WINDOWS_PROFILE_LIFECYCLE=true $(MAKE) pve-live-windows-accounts-check

.PHONY: pve-live-windows-helper-check
pve-live-windows-helper-check:
	@test -n "$$VC_WORKSPACE_LIVE_PVE_ENDPOINT"
	@test -n "$$VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE"
	@test -n "$$VC_WORKSPACE_LIVE_COMPUTER_VMID"
	@test -n "$$VC_WORKSPACE_LIVE_WINDOWS_HELPER_BINARY"
	@test -n "$$VC_WORKSPACE_LIVE_WINDOWS_HELPER_ARTIFACT_BIND"
	VC_WORKSPACE_LIVE_WINDOWS_HELPER=true go test ./internal/computer -run '^TestLiveWindowsInteractiveHelper$$' -v -count=1 -timeout 30m

.PHONY: pve-live-windows-expiry-check
pve-live-windows-expiry-check:
	@test -n "$$VC_WORKSPACE_LIVE_PVE_ENDPOINT"
	@test -n "$$VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE"
	@test -n "$$VC_WORKSPACE_LIVE_COMPUTER_VMID"
	@test -n "$$VC_WORKSPACE_LIVE_WINDOWS_HELPER_BINARY"
	@test -n "$$VC_WORKSPACE_LIVE_WINDOWS_HELPER_ARTIFACT_BIND"
	@test -n "$$VC_WORKSPACE_LIVE_WINDOWS_SESSION_WORKER"
	VC_WORKSPACE_LIVE_WINDOWS_ACCOUNT_EXPIRY=true go test ./internal/computer -run '^TestLiveWindowsAccountExpiry$$' -v -count=1 -timeout 15m

.PHONY: pve-live-windows-lease-check
pve-live-windows-lease-check:
	@test -n "$$VC_WORKSPACE_LIVE_PVE_ENDPOINT"
	@test -n "$$VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE"
	@test -n "$$VC_WORKSPACE_LIVE_COMPUTER_VMID"
	@test -n "$$VC_WORKSPACE_LIVE_WINDOWS_HELPER_BINARY"
	@test -n "$$VC_WORKSPACE_LIVE_WINDOWS_HELPER_ARTIFACT_BIND"
	@test -n "$$VC_WORKSPACE_LIVE_WINDOWS_SESSION_WORKER"
	VC_WORKSPACE_LIVE_WINDOWS_ACCOUNT_FENCE=true go test ./internal/computer -run '^TestLiveWindowsAccountLease$$' -v -count=1 -timeout 15m
