package pve

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// CloneLogTarget reads only the bounded log prefix. Unknown/older log formats
// return recognized=false, never a guessed target based on disk names. The
// declaration is evidence of intent, not proof of successful VM creation.
func (c *Client) CloneLogTarget(ctx context.Context, node, upid string) (source, target int, recognized bool, err error) {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`).MatchString(node) || !strings.HasPrefix(upid, "UPID:") || len(upid) > 1024 || strings.ContainsAny(upid, "/\\?#\r\n\x00") {
		return 0, 0, false, errors.New("invalid clone log target")
	}
	var response envelope[[]struct {
		N    int    `json:"n"`
		Text string `json:"t"`
	}]
	path := fmt.Sprintf("/nodes/%s/tasks/%s/log?start=0&limit=32", url.PathEscape(node), url.PathEscape(upid))
	if err := c.get(ctx, path, &response); err != nil {
		return 0, 0, false, err
	}
	if len(response.Data) > 32 {
		return 0, 0, false, errors.New("clone log prefix exceeded limit")
	}
	declaration := regexp.MustCompile(`^creating a clone of VM ([1-9][0-9]*) with ID ([1-9][0-9]*)$`)
	for i, line := range response.Data {
		if line.N != i+1 || len(line.Text) > 16384 {
			return 0, 0, false, errors.New("invalid clone log prefix")
		}
		match := declaration.FindStringSubmatch(line.Text)
		if match == nil {
			continue
		}
		if recognized {
			return 0, 0, false, errors.New("ambiguous clone log declarations")
		}
		source, err = strconv.Atoi(match[1])
		if err != nil {
			return 0, 0, false, errors.New("invalid clone source")
		}
		target, err = strconv.Atoi(match[2])
		if err != nil || target == source {
			return 0, 0, false, errors.New("invalid clone target")
		}
		recognized = true
	}
	return source, target, recognized, nil
}
