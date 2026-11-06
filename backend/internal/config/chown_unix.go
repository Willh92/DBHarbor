//go:build !windows

package config

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
)

func applyUnixSocketGroup(sockPath, groupSpec string) error {
	groupSpec = strings.TrimSpace(groupSpec)
	if groupSpec == "" {
		return nil
	}
	gid, err := strconv.Atoi(groupSpec)
	if err != nil {
		grp, lookupErr := user.LookupGroup(groupSpec)
		if lookupErr != nil {
			return fmt.Errorf("lookup unix group %q: %w", groupSpec, lookupErr)
		}
		gid, err = strconv.Atoi(grp.Gid)
		if err != nil {
			return fmt.Errorf("invalid gid %q for group %q: %w", grp.Gid, groupSpec, err)
		}
	}
	return os.Chown(sockPath, -1, gid)
}
