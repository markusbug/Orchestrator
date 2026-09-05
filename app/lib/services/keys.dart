import 'dart:convert';
import 'dart:typed_data';

import 'package:cryptography/cryptography.dart';
import 'package:flutter_secure_storage/flutter_secure_storage.dart';

/// An Ed25519 key pair for one host, stored in the Keychain.
class DeviceKey {
  DeviceKey(this._pair, this.publicKey);

  final SimpleKeyPair _pair;
  final Uint8List publicKey;

  String get publicKeyB64 => base64.encode(publicKey);

  Future<Uint8List> sign(List<int> message) async {
    final sig = await Ed25519().sign(message, keyPair: _pair);
    return Uint8List.fromList(sig.bytes);
  }

  static Future<DeviceKey> generate() async {
    final pair = await Ed25519().newKeyPair();
    final pub = await pair.extractPublicKey();
    return DeviceKey(pair, Uint8List.fromList(pub.bytes));
  }

  Future<String> serialize() async {
    final seed = await _pair.extractPrivateKeyBytes();
    return jsonEncode({
      'seed': base64.encode(seed),
      'pub': base64.encode(publicKey),
    });
  }

  static DeviceKey deserialize(String s) {
    final j = jsonDecode(s) as Map<String, dynamic>;
    final seed = base64.decode(j['seed'] as String);
    final pub = base64.decode(j['pub'] as String);
    final pair = SimpleKeyPairData(
      seed,
      publicKey: SimplePublicKey(pub, type: KeyPairType.ed25519),
      type: KeyPairType.ed25519,
    );
    return DeviceKey(pair, Uint8List.fromList(pub));
  }
}

/// Abstract secret store so tests can run without the Keychain.
abstract class SecretStore {
  Future<String?> read(String key);
  Future<void> write(String key, String value);
  Future<void> delete(String key);
}

class KeychainStore implements SecretStore {
  KeychainStore()
    : _storage = const FlutterSecureStorage(
        iOptions: IOSOptions(accessibility: KeychainAccessibility.first_unlock),
      );

  final FlutterSecureStorage _storage;

  @override
  Future<String?> read(String key) => _storage.read(key: key);

  @override
  Future<void> write(String key, String value) =>
      _storage.write(key: key, value: value);

  @override
  Future<void> delete(String key) => _storage.delete(key: key);
}

class MemorySecretStore implements SecretStore {
  final Map<String, String> _m = {};

  @override
  Future<String?> read(String key) async => _m[key];

  @override
  Future<void> write(String key, String value) async => _m[key] = value;

  @override
  Future<void> delete(String key) async => _m.remove(key);
}

/// Loads and saves per-host device keys.
class KeyService {
  KeyService(this._store);

  final SecretStore _store;
  final Map<String, DeviceKey> _cache = {};

  String _k(String hostId) => 'orchestrator.key.$hostId';

  Future<DeviceKey?> load(String hostId) async {
    final c = _cache[hostId];
    if (c != null) return c;
    final s = await _store.read(_k(hostId));
    if (s == null) return null;
    final k = DeviceKey.deserialize(s);
    _cache[hostId] = k;
    return k;
  }

  Future<void> save(String hostId, DeviceKey key) async {
    _cache[hostId] = key;
    await _store.write(_k(hostId), await key.serialize());
  }

  Future<void> delete(String hostId) async {
    _cache.remove(hostId);
    await _store.delete(_k(hostId));
  }
}
