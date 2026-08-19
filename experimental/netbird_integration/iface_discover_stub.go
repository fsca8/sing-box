//go:build !android

package netbird_integration

import nbembed "github.com/netbirdio/netbird/client/embed"

// SetAndroidIFaceDiscover is a no-op on non-Android platforms.
func SetAndroidIFaceDiscover(d nbembed.IFaceDiscover) {
}
