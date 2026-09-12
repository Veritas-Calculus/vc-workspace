package pve

import (
	"strings"
	"testing"
)

func TestMatchesGPUResourceMapping(t *testing.T) {
	for _, raw := range []string{
		"mapping=gpu,pcie=1,x-vga=1", "mapping=gpu,x-vga=1,pcie=1",
		"pcie=1,mapping=gpu,x-vga=1", "pcie=1,x-vga=1,mapping=gpu",
		"x-vga=1,mapping=gpu,pcie=1", "x-vga=1,pcie=1,mapping=gpu",
	} {
		if !MatchesGPUResourceMapping(raw, "gpu", "") {
			t.Errorf("order rejected: %s", raw)
		}
	}
	for _, raw := range []string{"mapping=gpu,mdev=i915-GVTg_V5_4", "mdev=i915-GVTg_V5_4,mapping=gpu"} {
		if !MatchesGPUResourceMapping(raw, "gpu", "i915-GVTg_V5_4") {
			t.Errorf("mdev order rejected: %s", raw)
		}
	}
	for _, raw := range []string{"", "gpu", "mapping=gpu", "mapping=gpu,pcie=1", "mapping=other,pcie=1,x-vga=1", "mapping=gpu,pcie=1,pcie=1", "mapping=gpu,pcie=0,x-vga=1", "mapping=gpu,pcie=1,x-vga=1,rombar=0", "mapping=gpu,pcie=1,x-vga=1,", "mapping=gpu,pcie=1,x-vga=1=1", "mapping=gpu,pcie=1,x-vga", " mapping=gpu,pcie=1,x-vga=1", strings.Repeat("a", 1025)} {
		if MatchesGPUResourceMapping(raw, "gpu", "") {
			t.Errorf("unsafe PCI match: %s", raw)
		}
	}
	for _, raw := range []string{"mapping=gpu", "mapping=gpu,mdev=other", "mapping=gpu,mapping=gpu", "mapping=gpu,mdev=i915-GVTg_V5_4,pcie=1", "mapping=gpu,mdev="} {
		if MatchesGPUResourceMapping(raw, "gpu", "i915-GVTg_V5_4") {
			t.Errorf("unsafe mdev match: %s", raw)
		}
	}
	if MatchesGPUResourceMapping("mapping=,pcie=1,x-vga=1", "", "") {
		t.Fatal("empty mapping accepted")
	}
}
