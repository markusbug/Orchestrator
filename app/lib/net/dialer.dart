import 'dart:async';

import '../model/host.dart';
import 'host_client.dart';

/// A connected [HostClient] and the address it was reached at.
class Dialed {
  const Dialed(this.client, this.addr);

  final HostClient client;
  final HostAddr addr;
}

/// Every address failed. [failures] is in the order the dials finished.
class DialFailed implements Exception {
  DialFailed(this.failures);

  final Map<HostAddr, Object> failures;

  /// The most recent failure, for the one-line status the UI shows.
  Object get last => failures.values.last;

  @override
  String toString() => failures.entries
      .map((e) => '${e.key.ip}: ${describeDialError(e.value)}')
      .join('; ');
}

/// Short human description of a dial or protocol error.
String describeDialError(Object e) {
  final s = e.toString();
  if (e is TimeoutException) return 'timed out';
  if (e is FingerprintMismatch) return 'certificate does not match';
  if (e is DialFailed) return describeDialError(e.last);
  final m = RegExp(r'OS Error: ([^,]+)').firstMatch(s);
  if (m != null) return m.group(1)!.toLowerCase();
  if (s.contains('SocketException')) return 'unreachable';
  return s.replaceFirst(RegExp(r'^\w+(Exception|Error): '), '');
}

/// Opens TLS to the first address that answers.
///
/// Direct addresses (LAN, Tailscale) are dialled together, so a stale entry
/// does not hold up a live one. Relay addresses start only after
/// [relayHeadStart], or as soon as every direct dial has failed, so bytes
/// cross the relay only when no direct path works. Whichever connects first
/// wins; the others are closed as they complete.
///
/// Throws [FingerprintMismatch] as soon as any address presents a
/// certificate that is not the pinned one, and [DialFailed] when every
/// address failed.
Future<Dialed> dialFirst(
  List<HostAddr> addrs, {
  required int hostPort,
  required String fingerprint,
  Duration relayHeadStart = const Duration(milliseconds: 1500),
}) {
  if (addrs.isEmpty) {
    return Future.error(StateError('no addresses for this host'));
  }
  final done = Completer<Dialed>();
  final failures = <HostAddr, Object>{};
  final relays = addrs.where((a) => a.isRelay).toList();
  var relaysStarted = relays.isEmpty;
  var inFlight = 0;
  Timer? headStart;

  void finish() {
    headStart?.cancel();
  }

  late final void Function(HostAddr) launch;

  void startRelays() {
    if (relaysStarted || done.isCompleted) return;
    relaysStarted = true;
    headStart?.cancel();
    relays.forEach(launch);
  }

  launch = (HostAddr a) {
    inFlight++;
    HostClient.connect(
          ip: a.ip,
          port: a.portOr(hostPort),
          fingerprint: fingerprint,
          timeout: a.dialTimeout,
        )
        .then(
          (c) {
            if (done.isCompleted) {
              unawaited(c.close());
              return;
            }
            finish();
            done.complete(Dialed(c, a));
          },
          onError: (Object e) {
            failures[a] = e;
            if (e is FingerprintMismatch && !done.isCompleted) {
              finish();
              done.completeError(e);
            }
          },
        )
        .whenComplete(() {
          inFlight--;
          if (done.isCompleted) return;
          if (!relaysStarted) {
            if (inFlight == 0) startRelays();
            return;
          }
          if (inFlight == 0) {
            finish();
            done.completeError(DialFailed(failures));
          }
        });
  };

  for (final a in addrs) {
    if (!a.isRelay) launch(a);
  }
  if (!relaysStarted) {
    if (inFlight == 0) {
      startRelays();
    } else {
      headStart = Timer(relayHeadStart, startRelays);
    }
  }
  return done.future;
}
