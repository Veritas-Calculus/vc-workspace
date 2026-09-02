.PHONY: dev server web macos-app macos-check check test images-check images-live-check k8s-check container-build pve-check pve-live-gpu-check pve-live-linux-privilege-check pve-live-windows-privilege-check

GOCACHE ?= $(CURDIR)/.cache/go-build
export GOCACHE

dev:
	@echo "Run 'make server' and 'make web' in separate terminals."

server:
	go run ./apps/control-plane

web:
	pnpm --filter @vc-vdi/web dev

macos-app:
	cd clients/macos && ./scripts/build-app.sh release

macos-check:
	bash -n clients/macos/scripts/build-app.sh
	bash -n clients/macos/scripts/build-native-rdp.sh
	bash -n clients/macos/scripts/verify-app.sh
	plutil -lint clients/macos/App/Info.plist
	cd clients/macos && SWIFTPM_MODULECACHE_OVERRIDE=$(CURDIR)/.cache/swift CLANG_MODULE_CACHE_PATH=$(CURDIR)/.cache/swift swift test --disable-sandbox
	cd clients/macos && ./scripts/build-app.sh release
	clients/macos/scripts/verify-app.sh

check: pve-check macos-check
	go test ./...
	go vet ./...
	cargo test --workspace
	pnpm check
	pnpm test
	pnpm build

test:
	go test ./...
	pnpm test

images-check:
	find deploy/images -type f -name '*.sh' -print0 | xargs -0 -n1 bash -n
	packer fmt -check -recursive deploy/images
	packer validate -syntax-only deploy/images/debian-13-xfce/debian-13-xfce.pkr.hcl
	packer validate -syntax-only deploy/images/windows-client/windows-client.pkr.hcl

images-live-check:
	test -n "$(VC_VDI_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_VDI_LIVE_PVE_CREDENTIAL_FILE)"
	VC_VDI_LIVE_TEMPLATE_AUDIT=true go test ./internal/pve -run TestLiveMaintainedDesktopTemplates -v

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
	test -n "$(VC_VDI_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_VDI_LIVE_PVE_CREDENTIAL_FILE)"
	VC_VDI_LIVE_GPU_AUDIT=true go test ./internal/pve -run TestLiveGPUInventory -v

pve-live-linux-privilege-check:
	test -n "$(VC_VDI_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_VDI_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_VDI_LIVE_PRIVILEGE_VMID)"
	VC_VDI_LIVE_PRIVILEGE_AUDIT=true go test ./internal/httpapi -run TestLiveLinuxDesktopPrivilegePolicy -v

pve-live-windows-privilege-check:
	test -n "$(VC_VDI_LIVE_PVE_ENDPOINT)"
	test -n "$(VC_VDI_LIVE_PVE_CREDENTIAL_FILE)"
	test -n "$(VC_VDI_LIVE_PRIVILEGE_VMID)"
	VC_VDI_LIVE_WINDOWS_PRIVILEGE_AUDIT=true go test ./internal/httpapi -run TestLiveWindowsDesktopPrivilegePolicy -v
