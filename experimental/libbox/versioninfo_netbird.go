// experimental/libbox/versioninfo_netbird.go
//go:build with_netbird

package libbox

import netbirdversion "github.com/netbirdio/netbird/version"

// NetbirdVersion returns the netbird engine version (upstream tag, e.g. "v0.37.3").
func NetbirdVersion() string {
	return netbirdversion.NetbirdVersion()
}

// NetbirdCommit returns the VCS revision (12 chars) of the netbird build,
// or "" when no build info is embedded.
func NetbirdCommit() string {
	return netbirdversion.NetbirdCommit()
}
