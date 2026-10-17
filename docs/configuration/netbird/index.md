# Netbird

Netbird overlay network integration, compiled into the unified binary with the `with_netbird` build tag.

The Netbird client runs **inside the sing-box process**: there is no separate Netbird daemon, no second
client installing its own kernel routes, and only one process to supervise. The overlay's custom domains
and CIDR are injected into the sing-box DNS and route rules.

## Configuration

The integration has its own configuration file, **in the same directory as the sing-box config file**:

```text
netbird-config.json
```

The file is a separate document on purpose: switching sing-box profiles never touches the Netbird
identity, and the overlay configuration has one obvious home instead of hiding in the sing-box config.

Enable it per run:

```sh
sing-box run-all -c /path/to/sing-box-config.json --enable-netbird
# or pick another config file
sing-box run-all -c /path/to/sing-box-config.json --enable-netbird --netbird-config /path/to/netbird-config.json
```

If the file is missing, the credentials are taken from the environment (`NB_SETUP_KEY`, `NB_JWT_TOKEN`,
`NB_PRIVATE_KEY`, `NB_DEVICE_NAME`, `NB_MANAGEMENT_URL`, `NB_LOG_LEVEL`).

If no credentials are available at all, the integration is skipped and sing-box runs unchanged.

### Structure

```json
{
  "device": {
    "name": "sing-netbird",
    "management_url": "https://api.netbird.io:443",
    "setup_key": "",
    "jwt_token": "",
    "private_key": ""
  },
  "network": {
    "kernel_tun": false,
    "mtu": 0,
    "wireguard_port": 0,
    "disable_ipv6": false,
    "disable_client_routes": false,
    "block_lan_access": false,
    "block_inbound": false,
    "nat_external_ips": [],
    "dns_labels": []
  },
  "security": {
    "pre_shared_key": "",
    "rosenpass": false,
    "rosenpass_permissive": false
  },
  "lazy_connection": null,
  "expose_ports": [
    {"port": 8022, "target": "127.0.0.1:8022"}
  ],
  "performance": {
    "preallocated_buffers_per_pool": 0,
    "max_batch_size": 0
  },
  "log": {
    "level": "info"
  }
}
```

### Fields

#### device

Identity and credentials of this peer.

* `name`: this peer's name in the network. `sing-netbird` is used by default.
* `management_url`: management server URL. `https://api.netbird.io:443` is used by default.
* Exactly one credential is normally set — `setup_key` (reusable or single-use setup key), `jwt_token`
  (JWT issued by the management server) or `private_key` (reuse an existing device identity).
  Credentials left empty fall back to `NB_SETUP_KEY` / `NB_JWT_TOKEN` / `NB_PRIVATE_KEY`.

#### network

Data path and addressing behaviour.

* `kernel_tun`: create the Linux kernel TUN device and a static route to it instead of using the userspace
  netstack. Requires root and is only honored on Linux; other platforms always use the netstack.
* `mtu`: tunnel interface MTU, `576`..`8192`. `0` keeps the stored/default value (1280).
* `wireguard_port`: local WireGuard listen port, `1`..`65535`. `0` picks a random port.
* `disable_ipv6`: disable IPv6 overlay addressing.
* `disable_client_routes`: do not install the routes learned from the management server. Use it when the
  overlay traffic should be steered only by the sing-box rules.
* `block_lan_access`: prevent this peer from reaching the host's local network (RFC 1918, link-local,
  loopback) while acting as a routing peer.
* `block_inbound`: reject all inbound connections from peers.
* `nat_external_ips`: map external IPs to internal IPs or interfaces for ICE candidate selection, in the
  `203.0.113.7/32=10.0.0.2/32` or `203.0.113.7/32=eth0` form.
* `dns_labels`: extra DNS labels owned by this peer.

#### security

* `pre_shared_key`: WireGuard pre-shared key of the tunnel interface.
* `rosenpass`: enable the Rosenpass post-quantum key exchange.
* `rosenpass_permissive`: keep a Rosenpass-enabled peer able to connect to peers without Rosenpass.

#### lazy_connection

Local override for Netbird's lazy connections.

Unset (`null`) defers to the management server's feature flag. `true` and `false` override it in both
directions — set `false` when the first request to a peer must not wait for an on-demand connection.

#### expose_ports

Overlay → local TCP port forwards, each entry an object with the overlay `port` to listen on and the local
`target` (`host:port`) to forward to.

```json
"expose_ports": [
  {"port": 8022, "target": "127.0.0.1:8022"},
  {"port": 8080, "target": "127.0.0.1:8080"}
]
```

The userspace netstack accepts overlay TCP connections but cannot deliver them to a process listening on
the host kernel stack, so an entry is required for every local service that peers should reach.
Ports must be unique; a target must be a `host:port` address.

#### performance

* `preallocated_buffers_per_pool`: cap the per-tunnel buffer pool (`0` keeps it unbounded).
* `max_batch_size`: packets read/written per syscall (`0` uses the platform default).

!!! note

    These two values are process-global in the Netbird embed API: a value set here also becomes the
    default for every client constructed later in the same process.

#### log

* `level`: `trace`, `debug`, `info`, `warn` or `error`. `info` is used by default.

The log destination is not configurable: the engine writes to `logs/netbird.log` in its data directory,
next to the sing-box log.

### Data directory

The data directory is not part of the configuration file — it is derived from the location of the sing-box
config file, so both engines always agree on where state lives:

```text
<data directory>/
├── sing-box-config.json
├── netbird-config.json
├── logs/
│   ├── sing-box.log
│   └── netbird.log
└── nb-state/            # engine state, do not edit
    ├── config.json
    ├── state.json
    ├── nb-domains.json
    └── nb-cidr.json
```

`nb-state/` holds the WireGuard key, the peer state, and the rule-set files referenced by the injected
sing-box configuration. It is engine-internal and regenerated as needed.

### Notes

Unknown keys and invalid values are errors: the engine reports the offending key and disables the
integration instead of starting with a silently dropped option. A configuration file in the flat format of
earlier versions is still accepted, with a warning naming the keys to migrate — `device_name` →
`device.name`, `setup_key` → `device.setup_key`, `jwt_token` → `device.jwt_token`, `management_url` →
`device.management_url`, `private_key` → `device.private_key`, `log_level` → `log.level`, `kernel_tun` →
`network.kernel_tun`. The old `admin_url`, `package_name` and `data_dir` keys were never read by the engine
and are ignored.

## Command line

`netbird-run` starts the integration standalone (for debugging), `netbird-status` prints its status:

```sh
sing-box netbird-run --netbird-config /path/to/netbird-config.json
sing-box netbird-status
```
