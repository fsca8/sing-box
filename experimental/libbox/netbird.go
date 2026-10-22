//go:build with_netbird

package libbox

import (
	"strconv"
	"sync"

	"github.com/sagernet/sing-box/experimental/netbird_integration"
)

// netbirdEngineMu 保护 netbirdEngine：Kotlin 线程（start/stop）与 Go 侧回调
// 可能并发，读改写不加锁会漏 Shutdown 或漏判旧引擎。
var (
	netbirdEngineMu sync.Mutex
	netbirdEngine   *netbird_integration.Engine
)

// netbirdParseConfig parses the engine config and applies the embed defaults
// every entry point needs (data dir = libbox base path, so the rule-set files
// and the engine state land in the app's private dir).
func netbirdParseConfig(netbirdConfigJSON string) (*netbird_integration.Config, error) {
	cfg, err := netbird_integration.ParseConfig(netbirdConfigJSON)
	if err != nil {
		return nil, err
	}
	// Inject basePath as DataDir if not already set (Android needs this for nb-state persistence)
	if cfg.DataDir == "" && sBasePath != "" {
		cfg.DataDir = sBasePath
		writeMarker("netbird: set DataDir=" + sBasePath)
	}
	return cfg, nil
}

// netbirdPrepare shuts down a previous engine and returns a parsed config with
// the Android bridges (protector / interface discovery) registered.
//
// 同一进程内重复启动(Android stop→start、hot enable、异常路径二次 start):
// 必须先停掉旧引擎。否则旧引擎仍持有同一 WireGuard key / mgmt 连接 / 状态
// 文件, 新引擎再拉一个同身份实例并存互踩 → netbird 隧道不可用。
func netbirdPrepare(netbirdConfigJSON string) (*netbird_integration.Config, error) {
	netbirdEngineMu.Lock()
	if netbirdEngine != nil {
		writeMarker("netbird: stopping previous engine (start over a live one)")
		netbirdEngine.Shutdown()
		netbirdEngine = nil
	}
	netbirdEngineMu.Unlock()

	cfg, err := netbirdParseConfig(netbirdConfigJSON)
	if err != nil {
		return nil, err
	}

	// 嵌入模式下 netbird 控制面 socket 需要绕过 VpnService TUN:
	// 把 Kotlin 注入的 Protector(VpnService.protect) 桥接给 netbird 引擎。
	if androidProtector != nil {
		netbird_integration.SetAndroidProtectFn(func(fd int32) bool {
			return androidProtector.Protect(fd)
		})
		writeMarker("netbird: android protect fn registered")
	} else {
		writeMarker("netbird: no android protector (netbird control-plane will use VpnService TUN)")
	}

	// Android 上标准库 netlink 接口枚举被 SELinux 禁止 → ICE 无 host 候选
	// → 全走 relay。把 Kotlin 注入的 IFaceDiscover(java NetworkInterface)
	// 桥接给 netbird 引擎, 必须在 engine.Start() 之前。
	registerIFaceDiscover()
	return cfg, nil
}

// netbirdRecordResult keeps the engine reachable for NetbirdStop().
func netbirdRecordResult(result *netbird_integration.StartAllResult) {
	if result.Engine != nil {
		netbirdEngineMu.Lock()
		netbirdEngine = result.Engine
		netbirdEngineMu.Unlock()
		// 异步启动: 引擎实例已创建, 但连接/启动发生在后台 goroutine —
		// 这里不代表启动成功(无网络时引擎会在后台重试/失败)。真实状态
		// 看 logs/netbird.log 的 "engine started"/"engine failed to start"。
		writeMarker("netbird: engine instance created (async start, status in logs/netbird.log)")
	} else if result.EngineErr != "" {
		writeMarker("netbird: engine failed to start: " + result.EngineErr)
	} else {
		writeMarker("netbird: no credentials, engine not started")
	}
}

// NetbirdStartAll takes netbird config JSON and sing-box config JSON,
// starts the netbird engine, syncs with management server, injects
// DNS/outbound/route rules into the sing-box config, and returns the
// modified sing-box config.
//
// If credentials are missing or incomplete, returns singBoxConfig unchanged.
func NetbirdStartAll(netbirdConfigJSON string, singBoxConfig string) (modified string, retErr error) {
	defer recoverError(&retErr)
	// panic 路径的返回值要给宿主一个可用的配置，别让它拿空串去落盘。
	modified = singBoxConfig
	writeMarker("NetbirdStartAll ENTERED")
	writeMarker("NetbirdStartAll: nbConfig length=" + strconv.Itoa(len(netbirdConfigJSON)))

	cfg, err := netbirdPrepare(netbirdConfigJSON)
	if err != nil {
		writeMarker("NetbirdStartAll: ParseConfig FAILED: " + err.Error())
		return singBoxConfig, err
	}

	result, err := netbird_integration.StartAll(cfg, []byte(singBoxConfig))
	if err != nil {
		writeMarker("NetbirdStartAll: StartAll FAILED: " + err.Error())
		return singBoxConfig, err
	}

	netbirdRecordResult(result)
	return string(result.ModifiedConfig), nil
}

// NetbirdStartEngine starts the engine only — the "hot enable" primitive.
// The scaffolding is already in the running sing-box config (NetbirdInjectOnly
// put it there), so nothing is injected and nothing has to be reloaded: the
// TUN stays up and traffic is not interrupted.
func NetbirdStartEngine(netbirdConfigJSON string) (retErr error) {
	defer recoverError(&retErr)
	writeMarker("NetbirdStartEngine ENTERED (hot enable)")

	cfg, err := netbirdPrepare(netbirdConfigJSON)
	if err != nil {
		writeMarker("NetbirdStartEngine: ParseConfig FAILED: " + err.Error())
		return err
	}

	result, err := netbird_integration.StartEngine(cfg)
	if err != nil {
		writeMarker("NetbirdStartEngine: StartEngine FAILED: " + err.Error())
		return err
	}

	netbirdRecordResult(result)
	return nil
}

// NetbirdInjectOnly injects the netbird scaffolding (dns server, outbound,
// rule-set references) WITHOUT starting the engine, and returns the modified
// sing-box config.
//
// Used when netbird is disabled at start: keeping the rules in the loaded
// config is what makes enabling it later cheap (NetbirdStartEngine, no reload,
// no TUN rebuild). While the engine is off the rules fail closed — the
// outbound returns context.Canceled, the DNS transport answers SERVFAIL.
func NetbirdInjectOnly(netbirdConfigJSON string, singBoxConfig string) (modified string, retErr error) {
	defer recoverError(&retErr)
	modified = singBoxConfig
	writeMarker("NetbirdInjectOnly ENTERED")

	// 注意用 netbirdParseConfig 而不是 netbirdPrepare: 这里不能停掉正在跑的
	// 引擎(关闭态注入时可能有一轮 hot start 正在进行)。
	cfg, err := netbirdParseConfig(netbirdConfigJSON)
	if err != nil {
		writeMarker("NetbirdInjectOnly: ParseConfig FAILED: " + err.Error())
		return singBoxConfig, err
	}

	result, err := netbird_integration.InjectOnly(cfg, []byte(singBoxConfig))
	if err != nil {
		writeMarker("NetbirdInjectOnly: InjectOnly FAILED: " + err.Error())
		return singBoxConfig, err
	}
	if len(result.ModifiedConfig) > 0 {
		modified = string(result.ModifiedConfig)
	}
	writeMarker("NetbirdInjectOnly: scaffolding injected (engine not started)")
	return modified, nil
}

// NetbirdStop stops the netbird engine and clears the integration state
// (global nb-out/DNS client, tunnel DNS address, network watcher).
//
// Must be called when the host service (Android VpnService) is torn down:
// BoxService.stop() only closes the sing-box service — without this the
// embed client keeps its WireGuard key / management connection / state
// files alive, and the next start pulls up a second engine with the same
// identity, which fights the first one (stop→start leaves netbird dead).
func NetbirdStop() (retErr error) {
	defer recoverError(&retErr)
	netbirdEngineMu.Lock()
	defer netbirdEngineMu.Unlock()
	writeMarker("NetbirdStop ENTERED")
	if netbirdEngine != nil {
		netbirdEngine.Shutdown()
		netbirdEngine = nil
		writeMarker("NetbirdStop: engine stopped")
	} else {
		writeMarker("NetbirdStop: no engine running")
	}
	return nil
}

// NetbirdBridgeTCP starts an overlay→local TCP bridge on the given port.
// port is the overlay port to listen on; target is the local host:port to
// forward to (e.g. "127.0.0.1:8022"). Returns an error string on failure,
// empty string on success. Idempotent per port.
func NetbirdBridgeTCP(port int, target string) (errStr string) {
	err := netbirdBridgeTCP(port, target)
	if err != nil {
		writeMarker("NetbirdBridgeTCP FAILED: " + err.Error())
		return err.Error()
	}
	writeMarker("NetbirdBridgeTCP OK: :" + strconv.Itoa(port) + " → " + target)
	return ""
}

func netbirdBridgeTCP(port int, target string) (retErr error) {
	defer recoverError(&retErr)
	_, err := netbird_integration.BridgeTCP(port, target)
	return err
}

// NetbirdStopBridge stops an active overlay→local TCP bridge.
func NetbirdStopBridge(port int) (errStr string) {
	err := netbirdStopBridge(port)
	if err != nil {
		writeMarker("NetbirdStopBridge FAILED: " + err.Error())
		return err.Error()
	}
	writeMarker("NetbirdStopBridge OK: :" + strconv.Itoa(port))
	return ""
}

func netbirdStopBridge(port int) (retErr error) {
	defer recoverError(&retErr)
	return netbird_integration.StopBridge(port)
}
