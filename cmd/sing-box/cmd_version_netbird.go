// cmd/sing-box/cmd_version_netbird.go
//go:build with_netbird

package main

import netbirdversion "github.com/netbirdio/netbird/version"

// netbirdVersionLine returns the netbird engine version line for
// `sing-box version` output, or "" when built without netbird.
func netbirdVersionLine() string {
	commit := netbirdversion.NetbirdCommit()
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if commit != "" {
		return "Netbird: " + netbirdversion.NetbirdVersion() + " (" + commit + ")\n"
	}
	return "Netbird: " + netbirdversion.NetbirdVersion() + "\n"
}
