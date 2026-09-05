import 'dart:convert';

import '../model/host.dart';
import '../protocol/challenge.dart';

/// Parses what the QR code contains: `orchestrator://pair?d=<base64url json>`,
/// or the raw JSON payload itself.
PairPayload? parsePairLink(String raw) {
  final s = raw.trim();
  if (s.isEmpty) return null;
  try {
    if (s.startsWith('{')) {
      return PairPayload.fromJson(jsonDecode(s) as Map<String, dynamic>);
    }
    final uri = Uri.parse(s);
    if (uri.scheme != 'orchestrator') return null;
    final d = uri.queryParameters['d'];
    if (d == null || d.isEmpty) return null;
    final json = utf8.decode(decodeB64(d));
    return PairPayload.fromJson(jsonDecode(json) as Map<String, dynamic>);
  } catch (_) {
    return null;
  }
}

/// Normalises a fingerprint typed by hand: lower case, `sha256:` prefix,
/// colons and spaces removed from the hex part.
String normalizeFingerprint(String s) {
  var t = s.trim().toLowerCase();
  if (t.startsWith('sha256:')) t = t.substring(7);
  t = t.replaceAll(RegExp(r'[^0-9a-f]'), '');
  return 'sha256:$t';
}
