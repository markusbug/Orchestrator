import 'dart:convert';
import 'dart:typed_data';

import 'package:flutter_test/flutter_test.dart';
import 'package:orchestrator/model/host.dart';
import 'package:orchestrator/model/session.dart';
import 'package:orchestrator/model/fs.dart';
import 'package:orchestrator/protocol/challenge.dart';
import 'package:orchestrator/protocol/frames.dart';
import 'package:orchestrator/services/pair_link.dart';

void main() {
  group('frames', () {
    test('round trip', () {
      final f = Frame.encode(kindInput, 0x01020304, [1, 2, 3]);
      expect(f, [1, 1, 2, 3, 4, 1, 2, 3]);
      final d = Frame.decode(Uint8List.fromList(f))!;
      expect(d.kind, kindInput);
      expect(d.handle, 0x01020304);
      expect(d.payload, [1, 2, 3]);
    });

    test('rejects short and unknown frames', () {
      expect(Frame.decode(Uint8List.fromList([2, 0, 0])), isNull);
      expect(Frame.decode(Uint8List.fromList([9, 0, 0, 0, 1])), isNull);
      expect(
        Frame.decode(Uint8List.fromList([2, 0, 0, 0, 1]))!.payload,
        isEmpty,
      );
    });
  });

  group('challenge', () {
    test('matches the daemon layout', () {
      final b = challengeBytes(
        serverNonce: [1, 2],
        clientNonce: [3],
        fingerprint: 'sha256:ab',
        deviceId: 'dev',
      );
      expect(b, [
        ...ascii.encode('orch-auth-v1'),
        1,
        2,
        3,
        ...ascii.encode('sha256:ab'),
        ...ascii.encode('dev'),
      ]);
    });

    test('decodes every base64 flavour', () {
      final raw = [250, 251, 252, 253, 254, 255, 0];
      expect(decodeB64(base64.encode(raw)), raw);
      expect(decodeB64(base64Url.encode(raw)), raw);
      expect(decodeB64(base64Url.encode(raw).replaceAll('=', '')), raw);
    });
  });

  group('pair link', () {
    final payload = {
      'v': 1,
      'host': 'laptop',
      'addrs': [
        {'ip': '192.168.1.5', 'kind': 'lan'},
        {'ip': '100.64.0.9', 'kind': 'tailscale'},
      ],
      'port': 7391,
      'fp': 'sha256:abc',
      'code': '123456',
      'expires_at': 4102444800,
    };

    test('parses the orchestrator:// URI the CLI prints', () {
      final uri =
          'orchestrator://pair?d=${base64Url.encode(utf8.encode(jsonEncode(payload))).replaceAll('=', '')}';
      final p = parsePairLink(uri)!;
      expect(p.host, 'laptop');
      expect(p.addrs.length, 2);
      expect(p.addrs.first.isLan, isTrue);
      expect(p.addrs.last.isTailscale, isTrue);
      expect(p.port, 7391);
      expect(p.fingerprint, 'sha256:abc');
      expect(p.code, '123456');
      expect(p.isExpired, isFalse);
    });

    test('parses raw JSON and rejects junk', () {
      expect(parsePairLink(jsonEncode(payload))!.code, '123456');
      expect(parsePairLink('https://example.com'), isNull);
      expect(parsePairLink('orchestrator://pair?d=%%%'), isNull);
      expect(parsePairLink(''), isNull);
    });

    test('normalises fingerprints', () {
      expect(normalizeFingerprint('SHA256:AB:CD ef'), 'sha256:abcdef');
      expect(normalizeFingerprint('abcdef'), 'sha256:abcdef');
    });
  });

  group('hosts', () {
    test('address order prefers last good, then LAN, then Tailscale', () {
      final h = HostRecord(
        id: 'x',
        name: 'x',
        hostname: 'x',
        addrs: const [
          HostAddr('100.64.0.1', 'tailscale'),
          HostAddr('10.0.0.2', 'lan'),
          HostAddr('10.0.0.3', 'lan'),
        ],
        port: 1,
        fingerprint: 'f',
        deviceId: 'd',
        createdAt: DateTime(2026),
      );
      expect(h.orderedAddrs.map((a) => a.ip), [
        '10.0.0.2',
        '10.0.0.3',
        '100.64.0.1',
      ]);
      h.lastGoodAddr = '100.64.0.1';
      expect(h.orderedAddrs.map((a) => a.ip), [
        '100.64.0.1',
        '10.0.0.2',
        '10.0.0.3',
      ]);
      final back = HostRecord.fromJson(
        jsonDecode(jsonEncode(h.toJson())) as Map<String, dynamic>,
      );
      expect(back.lastGoodAddr, '100.64.0.1');
    });

    test('relay address sorts last and keeps its own port', () {
      final h = HostRecord(
        id: 'x',
        name: 'x',
        hostname: 'x',
        addrs: const [
          HostAddr('abcd.relay.example', 'relay', port: 443),
          HostAddr('100.64.0.1', 'tailscale'),
          HostAddr('10.0.0.2', 'lan'),
        ],
        port: 7391,
        fingerprint: 'f',
        deviceId: 'd',
        createdAt: DateTime(2026),
      );
      expect(h.orderedAddrs.map((a) => a.ip), [
        '10.0.0.2',
        '100.64.0.1',
        'abcd.relay.example',
      ]);
      expect(h.relayAddr?.isRelay, isTrue);
      expect(h.relayAddr!.portOr(h.port), 443);
      expect(h.addrs[2].portOr(h.port), 7391);
      // A relay that worked last time is still tried first.
      h.lastGoodAddr = 'abcd.relay.example';
      expect(h.orderedAddrs.first.isRelay, isTrue);
      final back = HostRecord.fromJson(
        jsonDecode(jsonEncode(h.toJson())) as Map<String, dynamic>,
      );
      expect(
        back.relayAddr,
        const HostAddr('abcd.relay.example', 'relay', port: 443),
      );
      expect(back.addrs[2].port, isNull);
      // Older payloads without a port still parse.
      expect(HostAddr.fromJson({'ip': '1.2.3.4', 'kind': 'lan'}).port, isNull);
      final info = HostInfo.fromJson({
        'host': 'h',
        'addrs': [
          {'ip': 'abcd.relay.example', 'kind': 'relay', 'port': 443},
        ],
      });
      expect(info.addrs.single.port, 443);
      expect(back.addrs, h.addrs);
    });
  });

  group('sessions', () {
    SessionInfo mk(String id, String status, int last) => SessionInfo.fromJson({
      'id': id,
      'handle': 1,
      'name': id,
      'cwd': '/home/u/p',
      'cmd': 'claude',
      'args': [],
      'pid': 1,
      'status': status,
      'created_at': 1,
      'last_output_at': last,
      'cols': 80,
      'rows': 24,
    });

    test('sorts waiting, running by activity, then exited and stale', () {
      final s = sortSessions([
        mk('stale', 'stale', 100),
        mk('run-old', 'running', 10),
        mk('exited', 'exited', 200),
        mk('run-new', 'running', 50),
        mk('wait', 'waiting', 1),
      ]);
      expect(s.map((e) => e.id), [
        'wait',
        'run-new',
        'run-old',
        'exited',
        'stale',
      ]);
    });

    test('shortens paths under home', () {
      expect(shortenPath('/home/u/p', '/home/u'), '~/p');
      expect(shortenPath('/home/u', '/home/u'), '~');
      expect(shortenPath('/opt/x', '/home/u'), '/opt/x');
    });
  });
}
