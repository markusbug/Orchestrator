import 'dart:convert';
import 'dart:typed_data';

/// Bytes the phone signs to authenticate:
/// `"orch-auth-v1" || server_nonce || client_nonce || fingerprint || device_id`.
Uint8List challengeBytes({
  required List<int> serverNonce,
  required List<int> clientNonce,
  required String fingerprint,
  required String deviceId,
}) {
  final b = BytesBuilder(copy: false)
    ..add(ascii.encode('orch-auth-v1'))
    ..add(serverNonce)
    ..add(clientNonce)
    ..add(utf8.encode(fingerprint))
    ..add(utf8.encode(deviceId));
  return b.toBytes();
}

/// Decodes standard or URL base64 with or without padding, like the daemon.
Uint8List decodeB64(String s) {
  var t = s.trim().replaceAll('-', '+').replaceAll('_', '/');
  while (t.length % 4 != 0) {
    t += '=';
  }
  return base64.decode(t);
}
