package pve

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (c VMConfiguration) HasCloudInitDisk() bool {
	for _, disk := range c.Disks {
		volume, _, _ := strings.Cut(disk, ",")
		if strings.HasSuffix(volume, "-cloudinit") || strings.HasSuffix(volume, ":cloudinit") {
			return true
		}
	}
	return false
}

// ConfigureVMPrimaryDHCP deliberately targets only net0; this is not a static
// address allocator or a policy for extra NICs. The caller verifies ownership
// and absence of an existing ipconfig0 using the same configuration digest.
func (c *Client) ConfigureVMPrimaryDHCP(ctx context.Context, node string, vmid int, digest string) error {
	if strings.TrimSpace(node) == "" || vmid <= 0 || !ValidConfigurationDigest(digest) {
		return errors.New("cloud-init network precondition is invalid")
	}
	var response envelope[any]
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid), url.Values{"ipconfig0": {"ip=dhcp"}, "digest": {digest}}, &response)
}
