//go:build windows

package config

func applyUnixSocketGroup(_, _ string) error {
	return nil
}
