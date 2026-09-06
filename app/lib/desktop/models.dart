/// Dart mirrors of the JSON the `orchestrator` binary prints.
///
/// These follow `daemon/internal/admin/admin.go` and
/// `daemon/internal/protocol/protocol.go`; keep the field names in step with
/// the Go struct tags.
library;

class HostAddr {
  const HostAddr({required this.ip, required this.kind, required this.port});

  final String ip;
  final String kind; // lan | tailscale | relay
  final int port;

  factory HostAddr.fromJson(Map<String, dynamic> j) => HostAddr(
    ip: j['ip'] as String? ?? '',
    kind: j['kind'] as String? ?? '',
    port: (j['port'] as num?)?.toInt() ?? 0,
  );

  /// How the address reads to a person: the relay one is a long hex hostname
  /// that means nothing, so it gets a name instead.
  String get label => switch (kind) {
    'relay' => 'Anywhere, through the relay',
    'tailscale' => 'Tailscale',
    'lan' => 'Local network',
    _ => kind,
  };
}

class RelayStatus {
  const RelayStatus({
    required this.url,
    required this.addr,
    required this.port,
    required this.connected,
    required this.lastError,
    required this.streams,
  });

  final String url;
  final String addr;
  final int port;
  final bool connected;
  final String lastError;
  final int streams;

  factory RelayStatus.fromJson(Map<String, dynamic> j) => RelayStatus(
    url: j['url'] as String? ?? '',
    addr: j['addr'] as String? ?? '',
    port: (j['port'] as num?)?.toInt() ?? 0,
    connected: j['connected'] as bool? ?? false,
    lastError: j['last_error'] as String? ?? '',
    streams: (j['streams'] as num?)?.toInt() ?? 0,
  );
}

class DaemonStatus {
  const DaemonStatus({
    required this.version,
    required this.host,
    required this.port,
    required this.fingerprint,
    required this.addrs,
    required this.sessions,
    required this.devices,
    required this.uptimeSec,
    required this.pid,
    required this.configDir,
    required this.hostId,
    required this.relay,
  });

  final String version;
  final String host;
  final int port;
  final String fingerprint;
  final List<HostAddr> addrs;
  final int sessions;
  final int devices;
  final int uptimeSec;
  final int pid;
  final String configDir;
  final String hostId;
  final RelayStatus? relay;

  factory DaemonStatus.fromJson(Map<String, dynamic> j) => DaemonStatus(
    version: j['version'] as String? ?? '',
    host: j['host'] as String? ?? '',
    port: (j['port'] as num?)?.toInt() ?? 0,
    fingerprint: j['fingerprint'] as String? ?? '',
    addrs: ((j['addrs'] as List?) ?? const [])
        .map((e) => HostAddr.fromJson(e as Map<String, dynamic>))
        .toList(),
    sessions: (j['sessions'] as num?)?.toInt() ?? 0,
    devices: (j['devices'] as num?)?.toInt() ?? 0,
    uptimeSec: (j['uptime_sec'] as num?)?.toInt() ?? 0,
    pid: (j['pid'] as num?)?.toInt() ?? 0,
    configDir: j['config_dir'] as String? ?? '',
    hostId: j['host_id'] as String? ?? '',
    relay: j['relay'] == null
        ? null
        : RelayStatus.fromJson(j['relay'] as Map<String, dynamic>),
  );
}

class PairInfo {
  const PairInfo({
    required this.uri,
    required this.code,
    required this.fingerprint,
    required this.expiresAt,
    required this.addrs,
  });

  final String uri; // what the QR encodes
  final String code; // 6-digit manual fallback
  final String fingerprint;
  final DateTime expiresAt;
  final List<HostAddr> addrs;

  factory PairInfo.fromJson(Map<String, dynamic> j) => PairInfo(
    uri: j['uri'] as String? ?? '',
    code: j['code'] as String? ?? '',
    fingerprint: j['fp'] as String? ?? '',
    expiresAt: DateTime.fromMillisecondsSinceEpoch(
      ((j['expires_at'] as num?)?.toInt() ?? 0) * 1000,
    ),
    addrs: ((j['addrs'] as List?) ?? const [])
        .map((e) => HostAddr.fromJson(e as Map<String, dynamic>))
        .toList(),
  );

  bool get expired => DateTime.now().isAfter(expiresAt);
}

class Device {
  const Device({
    required this.id,
    required this.name,
    required this.createdAt,
    required this.lastSeenAt,
    required this.revoked,
  });

  final String id;
  final String name;
  final DateTime? createdAt;
  final DateTime? lastSeenAt;
  final bool revoked;

  factory Device.fromJson(Map<String, dynamic> j) => Device(
    id: j['id'] as String? ?? '',
    name: j['name'] as String? ?? '',
    createdAt: _time(j['created_at']),
    lastSeenAt: _time(j['last_seen_at']),
    revoked: j['revoked'] as bool? ?? false,
  );

  static DateTime? _time(Object? v) {
    final ms = (v as num?)?.toInt() ?? 0;
    return ms == 0 ? null : DateTime.fromMillisecondsSinceEpoch(ms);
  }
}

/// The state of the background service, from `orchestrator _service`.
class ServiceState {
  const ServiceState({
    required this.supported,
    required this.installed,
    required this.enabled,
    required this.running,
  });

  const ServiceState.unknown()
    : supported = false,
      installed = false,
      enabled = false,
      running = false;

  /// Whether this OS has a service implementation at all.
  final bool supported;

  /// Whether the unit or LaunchAgent has been written.
  final bool installed;

  /// Whether it starts at login.
  final bool enabled;

  /// Whether the daemon is answering right now.
  final bool running;

  factory ServiceState.fromJson(Map<String, dynamic> j) => ServiceState(
    supported: j['supported'] as bool? ?? false,
    installed: j['installed'] as bool? ?? false,
    enabled: j['enabled'] as bool? ?? false,
    running: j['running'] as bool? ?? false,
  );
}
