//go:build with_netbird

package netbird_integration

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// clearCredentialEnv removes the credential environment fallbacks so a test
// that expects "no credentials" cannot pass because of the developer's shell.
func clearCredentialEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"NB_SETUP_KEY", "NB_JWT_TOKEN", "NB_PRIVATE_KEY"} {
		t.Setenv(key, "")
	}
}

// captureLog collects the logrus output produced while fn runs.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buffer bytes.Buffer
	previous := log.StandardLogger().Out
	log.SetOutput(&buffer)
	defer log.SetOutput(previous)
	fn()
	return buffer.String()
}

func TestParseConfigGrouped(t *testing.T) {
	clearCredentialEnv(t)
	cfg, err := ParseConfig(`{
	  "device": {
	    "name": "my-laptop",
	    "management_url": "https://api.netbird.io:443",
	    "setup_key": "SETUP-KEY"
	  },
	  "network": {
	    "mtu": 1400,
	    "wireguard_port": 51820,
	    "disable_ipv6": true,
	    "disable_client_routes": true,
	    "block_lan_access": true,
	    "block_inbound": true,
	    "nat_external_ips": ["203.0.113.7/32=eth0"],
	    "dns_labels": ["laptop"]
	  },
	  "security": {
	    "pre_shared_key": "PSK",
	    "rosenpass": true,
	    "rosenpass_permissive": true
	  },
	  "lazy_connection": false,
	  "expose_ports": [{"port": 8022, "target": "127.0.0.1:8022"}],
	  "performance": {"preallocated_buffers_per_pool": 64, "max_batch_size": 32},
	  "log": {"level": "debug"}
	}`)
	require.NoError(t, err)
	require.Equal(t, "my-laptop", cfg.Device.Name)
	require.Equal(t, "https://api.netbird.io:443", cfg.Device.ManagementURL)
	require.Equal(t, "SETUP-KEY", cfg.Device.SetupKey)
	require.Equal(t, 1400, cfg.Network.MTU)
	require.Equal(t, 51820, cfg.Network.WireguardPort)
	require.True(t, cfg.Network.DisableIPv6)
	require.True(t, cfg.Network.DisableClientRoutes)
	require.True(t, cfg.Network.BlockLANAccess)
	require.True(t, cfg.Network.BlockInbound)
	require.Equal(t, []string{"203.0.113.7/32=eth0"}, cfg.Network.NATExternalIPs)
	require.Equal(t, []string{"laptop"}, cfg.Network.DNSLabels)
	require.Equal(t, "PSK", cfg.Security.PreSharedKey)
	require.True(t, cfg.Security.Rosenpass)
	require.True(t, cfg.Security.RosenpassPermissive)
	require.NotNil(t, cfg.LazyConnection)
	require.False(t, *cfg.LazyConnection)
	require.Equal(t, []ExposePortConfig{{Port: 8022, Target: "127.0.0.1:8022"}}, cfg.ExposePorts)
	require.Equal(t, uint32(64), cfg.Performance.PreallocatedBuffersPerPool)
	require.Equal(t, uint32(32), cfg.Performance.MaxBatchSize)
	require.Equal(t, "debug", cfg.Log.Level)
	// DataDir is never read from the file.
	require.Empty(t, cfg.DataDir)
}

func TestParseConfigDefaults(t *testing.T) {
	clearCredentialEnv(t)
	cfg, err := ParseConfig(`{"device": {"setup_key": "SETUP-KEY"}}`)
	require.NoError(t, err)
	require.Equal(t, defaultDeviceName, cfg.Device.Name)
	require.Equal(t, defaultManagementURL, cfg.Device.ManagementURL)
	require.Equal(t, defaultLogLevel, cfg.Log.Level)
	// Unset numeric options keep netbird's own defaults (0 = not passed on).
	require.Zero(t, cfg.Network.MTU)
	require.Zero(t, cfg.Network.WireguardPort)
	require.Zero(t, cfg.Performance.PreallocatedBuffersPerPool)
	require.Nil(t, cfg.LazyConnection)
	// An empty document is a valid, fully-defaulted config.
	empty, err := ParseConfig(`{}`)
	require.NoError(t, err)
	require.Equal(t, defaultDeviceName, empty.Device.Name)
}

func TestParseConfigRejectsUnknownKeys(t *testing.T) {
	clearCredentialEnv(t)
	for name, document := range map[string]string{
		"top level":  `{"device": {"setup_key": "K"}, "device_nmae": "typo"}`,
		"in device":  `{"device": {"setup_key": "K", "setupkey": "typo"}}`,
		"in network": `{"network": {"mtus": 1280}}`,
		"in log":     `{"log": {"levl": "debug"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig(document)
			require.Error(t, err)
			require.Contains(t, err.Error(), "unknown field")
		})
	}
}

func TestParseConfigMalformedJSON(t *testing.T) {
	clearCredentialEnv(t)
	for name, document := range map[string]string{
		"truncated":  `{"device": {"setup_key": `,
		"array":      `[1, 2, 3]`,
		"wrong type": `{"network": {"mtu": "large"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig(document)
			require.Error(t, err)
		})
	}
}

func TestParseConfigLegacyFlatFormat(t *testing.T) {
	clearCredentialEnv(t)
	// The exact shape written by earlier app versions.
	document := `{"setup_key":"SETUP-KEY","management_url":"https://api.netbird.io:443",` +
		`"device_name":"old-laptop","log_level":"info","expose_ports":[{"port":8022,"target":"127.0.0.1:8022"}]}`
	var cfg *Config
	var err error
	output := captureLog(t, func() {
		cfg, err = ParseConfig(document)
	})
	require.NoError(t, err)
	require.Equal(t, "SETUP-KEY", cfg.Device.SetupKey)
	require.Equal(t, "https://api.netbird.io:443", cfg.Device.ManagementURL)
	require.Equal(t, "old-laptop", cfg.Device.Name)
	require.Equal(t, "info", cfg.Log.Level)
	require.Equal(t, []ExposePortConfig{{Port: 8022, Target: "127.0.0.1:8022"}}, cfg.ExposePorts)
	require.Contains(t, output, "deprecated flat format")
	require.Contains(t, output, "device_name")
}

func TestParseConfigLegacyPrivateKeyOnly(t *testing.T) {
	clearCredentialEnv(t)
	cfg, err := ParseConfig(`{"private_key":"PRIVATE-KEY","kernel_tun":true}`)
	require.NoError(t, err)
	require.Equal(t, "PRIVATE-KEY", cfg.Device.PrivateKey)
	require.True(t, cfg.Network.KernelTun)
	require.True(t, cfg.hasCredentials())
}

func TestParseConfigLegacyIgnoredKeys(t *testing.T) {
	clearCredentialEnv(t)
	var cfg *Config
	var err error
	output := captureLog(t, func() {
		cfg, err = ParseConfig(`{"setup_key":"K","admin_url":"https://example.com",` +
			`"package_name":"com.example.app","data_dir":"/somewhere/else"}`)
	})
	require.NoError(t, err)
	require.Contains(t, output, "admin_url is ignored")
	require.Contains(t, output, "package_name is ignored")
	require.Contains(t, output, "data_dir is ignored")
	// data_dir must not redirect engine state.
	require.Empty(t, cfg.DataDir)
	require.Equal(t, filepath.Join(os.TempDir(), "nb-state"), cfg.stateDir())
}

func TestParseConfigGroupedWinsOverLegacy(t *testing.T) {
	clearCredentialEnv(t)
	cfg, err := ParseConfig(`{"setup_key":"OLD","device_name":"old","log_level":"warn",` +
		`"device":{"setup_key":"NEW","name":"new"},"log":{"level":"debug"}}`)
	require.NoError(t, err)
	require.Equal(t, "NEW", cfg.Device.SetupKey)
	require.Equal(t, "new", cfg.Device.Name)
	require.Equal(t, "debug", cfg.Log.Level)
	require.Equal(t, defaultManagementURL, cfg.Device.ManagementURL)
}

func TestParseConfigGroupedExposePortsIsNotLegacy(t *testing.T) {
	clearCredentialEnv(t)
	var cfg *Config
	var err error
	// expose_ports belongs to the current schema: using it must not emit the
	// flat-format deprecation warning.
	output := captureLog(t, func() {
		cfg, err = ParseConfig(`{"device":{"setup_key":"K"},"expose_ports":[{"port":8022,"target":"127.0.0.1:8022"}]}`)
	})
	require.NoError(t, err)
	require.Len(t, cfg.ExposePorts, 1)
	require.NotContains(t, output, "deprecated flat format")
}

func TestValidate(t *testing.T) {
	clearCredentialEnv(t)
	for name, document := range map[string]string{
		"mtu too small":        `{"network":{"mtu":100}}`,
		"mtu too large":        `{"network":{"mtu":9000}}`,
		"wireguard port 0-ish": `{"network":{"wireguard_port":-1}}`,
		"wireguard port high":  `{"network":{"wireguard_port":70000}}`,
		"management url no host": `{"device":{"management_url":"https://"}}`,
		"management url scheme":  `{"device":{"management_url":"ftp://example.com"}}`,
		"expose port zero":       `{"expose_ports":[{"port":0,"target":"127.0.0.1:8022"}]}`,
		"expose port high":       `{"expose_ports":[{"port":70000,"target":"127.0.0.1:8022"}]}`,
		"expose port duplicate":  `{"expose_ports":[{"port":8022,"target":"127.0.0.1:8022"},{"port":8022,"target":"127.0.0.1:8023"}]}`,
		"expose target no port":  `{"expose_ports":[{"port":8022,"target":"/"}]}`,
		"expose target empty":    `{"expose_ports":[{"port":8022}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig(document)
			require.Error(t, err)
		})
	}
	// Boundaries are accepted, and kernel_tun is a warning on non-Linux only.
	cfg, err := ParseConfig(`{"network":{"mtu":576,"wireguard_port":1,"kernel_tun":true},` +
		`"expose_ports":[{"port":1,"target":"127.0.0.1:8022"}]}`)
	require.NoError(t, err)
	require.Equal(t, 576, cfg.Network.MTU)
	cfg, err = ParseConfig(`{"network":{"mtu":8192,"wireguard_port":65535}}`)
	require.NoError(t, err)
	require.Equal(t, 8192, cfg.Network.MTU)
}

func TestValidateWarnsOnMultipleCredentials(t *testing.T) {
	clearCredentialEnv(t)
	output := captureLog(t, func() {
		_, err := ParseConfig(`{"device":{"setup_key":"A","jwt_token":"B"}}`)
		require.NoError(t, err)
	})
	require.Contains(t, output, "more than one credential")
}

func TestHasCredentials(t *testing.T) {
	clearCredentialEnv(t)
	require.False(t, (*Config)(nil).hasCredentials())
	require.False(t, (&Config{}).hasCredentials())
	for _, testCase := range []struct {
		name string
		cfg  Config
	}{
		{"setup key", Config{Device: DeviceOptions{SetupKey: "A"}}},
		{"jwt token", Config{Device: DeviceOptions{JWTToken: "A"}}},
		{"private key", Config{Device: DeviceOptions{PrivateKey: "A"}}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.True(t, testCase.cfg.hasCredentials())
		})
	}
	t.Run("environment", func(t *testing.T) {
		t.Setenv("NB_PRIVATE_KEY", "A")
		require.True(t, (&Config{}).hasCredentials())
	})
	t.Run("environment empty value", func(t *testing.T) {
		t.Setenv("NB_SETUP_KEY", "")
		require.False(t, (&Config{}).hasCredentials())
	})
}

func TestEnvFallback(t *testing.T) {
	t.Setenv("NB_SETUP_KEY", "FROM-ENV")
	require.Equal(t, "FROM-ENV", envFallback("", "NB_SETUP_KEY"))
	require.Equal(t, "FROM-CONFIG", envFallback("FROM-CONFIG", "NB_SETUP_KEY"))
	require.Equal(t, "", envFallback("", "NB_JWT_TOKEN"))
}

func TestOptionalUint32(t *testing.T) {
	require.Nil(t, optionalUint32(0))
	value := optionalUint32(7)
	require.NotNil(t, value)
	require.Equal(t, uint32(7), *value)
}

func TestLoadConfigFile(t *testing.T) {
	clearCredentialEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "netbird-config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"device":{"setup_key":"K"}}`), 0o600))
	cfg, err := LoadConfigFile(path)
	require.NoError(t, err)
	require.Equal(t, "K", cfg.Device.SetupKey)
	// The data directory is the caller's business, not the file's.
	require.Empty(t, cfg.DataDir)

	_, err = LoadConfigFile(filepath.Join(dir, "missing.json"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))

	broken := filepath.Join(dir, "broken.json")
	require.NoError(t, os.WriteFile(broken, []byte(`{"device":{"setup_key":"K"},"nope":1}`), 0o600))
	_, err = LoadConfigFile(broken)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown field")
	require.True(t, strings.Contains(err.Error(), "broken.json"))
}

func TestConfigDirectories(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{DataDir: dir}
	require.Equal(t, dir, cfg.dataDir())
	require.Equal(t, filepath.Join(dir, "nb-state"), cfg.stateDir())
	require.Equal(t, filepath.Join(dir, "logs"), cfg.logsDir())
	// Rule-set files live with the rest of the engine state.
	require.Equal(t, filepath.Join(dir, "nb-state", domainsRuleSetFile), domainsRuleSetPath(cfg))
	require.Equal(t, filepath.Join(dir, "nb-state", cidrRuleSetFile), cidrRuleSetPath(cfg))

	fallback := &Config{}
	require.Equal(t, os.TempDir(), fallback.dataDir())
	require.Equal(t, filepath.Join(os.TempDir(), "nb-state"), fallback.stateDir())
	require.Equal(t, filepath.Join(os.TempDir(), "logs"), fallback.logsDir())
}
