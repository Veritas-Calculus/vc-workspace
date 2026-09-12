package imagebuilder

import (
	"slices"
	"testing"
)

func TestInstallerHTTPConfigurationIsBoundedAndOnlyPassedToDebian(t *testing.T) {
	b := testBuilder(t)
	config := b.config
	config.HTTPBindAddress, config.HTTPInterface, config.HTTPPortRange = "10.31.0.128", "", "18840-18840"
	b, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := b.commandSpec(validDebianRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"PKR_VAR_http_bind_address=10.31.0.128", "PKR_VAR_http_interface=", "PKR_VAR_http_port_min=18840", "PKR_VAR_http_port_max=18840"} {
		if !slices.Contains(spec.env, expected) {
			t.Fatal("missing installer setting", expected)
		}
	}
	config.HTTPBindAddress, config.HTTPInterface = "", "en7"
	b, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	spec, err = b.commandSpec(validDebianRequest())
	if err != nil || !slices.Contains(spec.env, "PKR_VAR_http_bind_address=") || !slices.Contains(spec.env, "PKR_VAR_http_interface=en7") {
		t.Fatal("interface mode must not also set a bind address", err)
	}
	for _, test := range []struct{ bind, iface, ports string }{
		{"10.31.0.128", "en7", "8840-8847"}, {"0.0.0.0", "en7", "8840-8847"},
		{"hostname.invalid", "", "8840-8847"}, {"::1", "", "8840-8847"},
		{"224.0.0.1", "", "8840-8847"}, {"", "en7\n", "8840-8847"},
		{"0.0.0.0", "", "80-80"}, {"0.0.0.0", "", "9000-8000"},
		{"0.0.0.0", "", "8840-8872"}, {"0.0.0.0", "", "8840-65536"},
		{"0.0.0.0", "", "+8840-8847"}, {"0.0.0.0", "", "08840-8847"},
		{"0.0.0.0", "", "8840-8847-8848"},
	} {
		candidate := config
		candidate.HTTPBindAddress, candidate.HTTPInterface, candidate.HTTPPortRange = test.bind, test.iface, test.ports
		if _, err := New(candidate); err == nil {
			t.Fatal("unsafe installer setting accepted", test)
		}
	}
	defaultBuilder := testBuilder(t)
	if defaultBuilder.httpMin != 8840 || defaultBuilder.httpMax != 8847 {
		t.Fatal("missing bounded defaults")
	}
}
