//go:build with_netbird

package netbird_integration

import (
	"context"
	"fmt"
	"time"

	nbembed "github.com/netbirdio/netbird/client/embed"
	mgmProto "github.com/netbirdio/netbird/shared/management/proto"
)

// WaitSyncResponse polls GetLatestSyncResponse until a non-nil response
// is available, or until the context is cancelled.
func WaitSyncResponse(ctx context.Context, client *nbembed.Client) (*mgmProto.SyncResponse, error) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
			resp, err := client.GetLatestSyncResponse()
			if err != nil {
				// sync persistence might not be enabled yet
				continue
			}
			if resp != nil {
				return resp, nil
			}
		}
	}
}

// ExtractDomainsFromSync extracts custom domain names from the netbird
// SyncResponse DNS configuration. Returns domains from both NameServerGroups
// and CustomZones.
func ExtractDomainsFromSync(resp *mgmProto.SyncResponse) []string {
	if resp == nil {
		return nil
	}
	dnsCfg := resp.GetNetworkMap().GetDNSConfig()
	if dnsCfg == nil {
		return nil
	}

	seen := make(map[string]bool)
	var domains []string

	// Collect from NameServerGroups
	for _, nsg := range dnsCfg.GetNameServerGroups() {
		if nsg == nil {
			continue
		}
		for _, d := range nsg.GetDomains() {
			if d != "" && !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}

	// Collect from CustomZones
	for _, cz := range dnsCfg.GetCustomZones() {
		if cz == nil {
			continue
		}
		d := cz.GetDomain()
		if d != "" && !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}

	return domains
}

// WaitAndExtractDomains waits for netbird to sync and returns custom domains.
// timeout is the maximum time to wait for the initial sync.
func WaitAndExtractDomains(client *nbembed.Client, timeout time.Duration) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	resp, err := WaitSyncResponse(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("wait sync: %w", err)
	}

	domains := ExtractDomainsFromSync(resp)
	return domains, nil
}
