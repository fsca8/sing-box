//go:build !with_netbird

package libbox

// SetIFaceDiscover is a no-op when built without netbird integration.
func SetIFaceDiscover(d IFaceDiscover) {
}
