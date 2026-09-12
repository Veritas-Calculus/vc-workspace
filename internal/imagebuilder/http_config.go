package imagebuilder

import (
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
)

var interfaceName = regexp.MustCompile(`\A[A-Za-z0-9][A-Za-z0-9._:-]{0,63}\z`)

func validateHTTPConfig(config Config) (int, int, error) {
	invalid := errors.New("image HTTP server requires either an IPv4 bind address or an interface, and at most 32 unprivileged ports")
	if config.HTTPBindAddress != "" && config.HTTPInterface != "" {
		return 0, 0, invalid
	}
	if config.HTTPBindAddress != "" {
		ip := net.ParseIP(config.HTTPBindAddress)
		if ip == nil || ip.To4() == nil || strings.Contains(config.HTTPBindAddress, ":") || ip.IsMulticast() {
			return 0, 0, invalid
		}
	}
	if config.HTTPInterface != "" && !interfaceName.MatchString(config.HTTPInterface) {
		return 0, 0, invalid
	}
	minimum, maximum, found := strings.Cut(config.HTTPPortRange, "-")
	low, lowErr := strconv.Atoi(minimum)
	high, highErr := strconv.Atoi(maximum)
	if !found || lowErr != nil || highErr != nil || strconv.Itoa(low) != minimum || strconv.Itoa(high) != maximum ||
		low < 1024 || high > 65535 || high < low || high-low >= 32 {
		return 0, 0, invalid
	}
	return low, high, nil
}
