/// One address a host can be reached at. `kind` is `lan`, `tailscale`, or
/// `relay`. A relay address is a DNS name (`<hostid>.<relay-domain>`) that
/// carries its own [port]; the others use the host's port.
class HostAddr {
  const HostAddr(this.ip, this.kind, {this.port});

  final String ip;
  final String kind;
  final int? port;

  bool get isLan => kind == 'lan';
  bool get isTailscale => kind == 'tailscale';
  bool get isRelay => kind == 'relay';

  /// Port to dial, falling back to the host's default.
  int portOr(int hostPort) => port ?? hostPort;

  /// TLS connect timeout. The relay adds a round trip to the daemon before
  /// TLS starts, so it gets longer.
  Duration get dialTimeout =>
      isRelay ? const Duration(seconds: 12) : const Duration(seconds: 6);

  /// Short label for lists and status lines.
  String label(int hostPort) => isRelay ? 'relay' : '$ip:${portOr(hostPort)}';

  factory HostAddr.fromJson(Map<String, dynamic> j) => HostAddr(
    j['ip'] as String,
    (j['kind'] as String?) ?? 'lan',
    port: (j['port'] as num?)?.toInt(),
  );

  Map<String, dynamic> toJson() => {
    'ip': ip,
    'kind': kind,
    if (port != null) 'port': port,
  };

  @override
  bool operator ==(Object other) =>
      other is HostAddr &&
      other.ip == ip &&
      other.kind == kind &&
      other.port == port;

  @override
  int get hashCode => Object.hash(ip, kind, port);

  @override
  String toString() => port == null ? '$ip ($kind)' : '$ip:$port ($kind)';
}

/// Merges address lists, keeping order and dropping duplicates. Used to
/// keep an address that demonstrably worked even when the daemon does not
/// list it (loopback, NAT, a VPN the host cannot see).
List<HostAddr> mergeAddrs(
  Iterable<HostAddr> primary,
  Iterable<HostAddr> extra,
) {
  final out = <HostAddr>[];
  for (final a in [...primary, ...extra]) {
    if (!out.any((o) => o.ip == a.ip)) out.add(a);
  }
  return out;
}

/// What the pairing QR code carries.
class PairPayload {
  const PairPayload({
    required this.host,
    required this.addrs,
    required this.port,
    required this.fingerprint,
    required this.code,
    this.expiresAt,
  });

  final String host;
  final List<HostAddr> addrs;
  final int port;
  final String fingerprint;
  final String code;
  final int? expiresAt; // unix seconds

  factory PairPayload.fromJson(Map<String, dynamic> j) => PairPayload(
    host: (j['host'] as String?) ?? '',
    addrs: ((j['addrs'] as List?) ?? const [])
        .map((e) => HostAddr.fromJson(e as Map<String, dynamic>))
        .toList(),
    port: (j['port'] as num?)?.toInt() ?? 7391,
    fingerprint: (j['fp'] as String?) ?? (j['fingerprint'] as String?) ?? '',
    code: (j['code'] as String?) ?? '',
    expiresAt: (j['expires_at'] as num?)?.toInt(),
  );

  bool get isExpired =>
      expiresAt != null &&
      DateTime.now().millisecondsSinceEpoch ~/ 1000 > expiresAt!;
}

/// A paired host as persisted on the phone. The private key lives in the
/// Keychain under [id]; everything here is non-secret.
class HostRecord {
  HostRecord({
    required this.id,
    required this.name,
    required this.hostname,
    required this.addrs,
    required this.port,
    required this.fingerprint,
    required this.deviceId,
    required this.createdAt,
    this.lastGoodAddr,
  });

  /// Local identifier; equals the device id the daemon assigned at pairing.
  final String id;
  String name;
  String hostname;
  List<HostAddr> addrs;
  int port;
  final String fingerprint;
  final String deviceId;
  final DateTime createdAt;
  String? lastGoodAddr;

  factory HostRecord.fromJson(Map<String, dynamic> j) => HostRecord(
    id: j['id'] as String,
    name: j['name'] as String,
    hostname: (j['hostname'] as String?) ?? (j['name'] as String),
    addrs: ((j['addrs'] as List?) ?? const [])
        .map((e) => HostAddr.fromJson(e as Map<String, dynamic>))
        .toList(),
    port: (j['port'] as num).toInt(),
    fingerprint: j['fingerprint'] as String,
    deviceId: j['device_id'] as String,
    createdAt: DateTime.fromMillisecondsSinceEpoch(
      (j['created_at'] as num?)?.toInt() ?? 0,
    ),
    lastGoodAddr: j['last_good_addr'] as String?,
  );

  Map<String, dynamic> toJson() => {
    'id': id,
    'name': name,
    'hostname': hostname,
    'addrs': addrs.map((a) => a.toJson()).toList(),
    'port': port,
    'fingerprint': fingerprint,
    'device_id': deviceId,
    'created_at': createdAt.millisecondsSinceEpoch,
    if (lastGoodAddr != null) 'last_good_addr': lastGoodAddr,
  };

  /// Addresses in the order to try: last known good direct address, LAN,
  /// Tailscale, then anything else, with the relay last. The relay always
  /// works but is the slowest path and crosses a third party, so direct
  /// addresses get their chance first even after a relay session.
  List<HostAddr> get orderedAddrs {
    final out = <HostAddr>[];
    final good = lastGoodAddr;
    if (good != null) {
      for (final a in addrs) {
        if (a.ip == good && !a.isRelay) out.add(a);
      }
    }
    for (final a in addrs) {
      if (a.isLan && !out.contains(a)) out.add(a);
    }
    for (final a in addrs) {
      if (a.isTailscale && !out.contains(a)) out.add(a);
    }
    for (final a in addrs) {
      if (!a.isRelay && !out.contains(a)) out.add(a);
    }
    for (final a in addrs) {
      if (!out.contains(a)) out.add(a);
    }
    return out;
  }

  /// The relay address, if the host advertises one.
  HostAddr? get relayAddr {
    for (final a in addrs) {
      if (a.isRelay) return a;
    }
    return null;
  }
}

/// `host.info` reply.
class HostInfo {
  const HostInfo({
    required this.host,
    required this.version,
    required this.fingerprint,
    required this.port,
    required this.addrs,
    required this.roots,
    required this.defaultCmd,
    required this.home,
  });

  final String host;
  final String version;
  final String fingerprint;
  final int port;
  final List<HostAddr> addrs;
  final List<String> roots;
  final String defaultCmd;
  final String home;

  factory HostInfo.fromJson(Map<String, dynamic> j) => HostInfo(
    host: (j['host'] as String?) ?? '',
    version: (j['version'] as String?) ?? '',
    fingerprint: (j['fingerprint'] as String?) ?? '',
    port: (j['port'] as num?)?.toInt() ?? 7391,
    addrs: ((j['addrs'] as List?) ?? const [])
        .map((e) => HostAddr.fromJson(e as Map<String, dynamic>))
        .toList(),
    roots: ((j['roots'] as List?) ?? const []).cast<String>(),
    defaultCmd: (j['default_cmd'] as String?) ?? 'claude',
    home: (j['home'] as String?) ?? '',
  );
}
