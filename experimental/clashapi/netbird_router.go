//go:build with_netbird

package clashapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"

	"github.com/sagernet/sing-box/experimental/netbird_integration"
)

// netbirdRouter exposes netbird engine state under /netbird/*.
// Returns nil when the netbird integration is not compiled in.
func netbirdRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/peers", func(w http.ResponseWriter, r *http.Request) {
		render.JSON(w, r, netbird_integration.PeerStatuses())
	})
	return r
}
