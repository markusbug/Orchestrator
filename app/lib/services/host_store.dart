import 'dart:convert';

import 'package:shared_preferences/shared_preferences.dart';

import '../model/host.dart';

/// Persists the list of paired hosts (non-secret data) in preferences.
abstract class HostStore {
  Future<List<HostRecord>> load();
  Future<void> save(List<HostRecord> hosts);
}

class PrefsHostStore implements HostStore {
  static const _key = 'orchestrator.hosts';

  @override
  Future<List<HostRecord>> load() async {
    final p = await SharedPreferences.getInstance();
    final raw = p.getString(_key);
    if (raw == null || raw.isEmpty) return [];
    try {
      final list = jsonDecode(raw) as List;
      return list
          .map((e) => HostRecord.fromJson(e as Map<String, dynamic>))
          .toList();
    } catch (_) {
      return [];
    }
  }

  @override
  Future<void> save(List<HostRecord> hosts) async {
    final p = await SharedPreferences.getInstance();
    await p.setString(_key, jsonEncode(hosts.map((h) => h.toJson()).toList()));
  }
}

class MemoryHostStore implements HostStore {
  List<HostRecord> hosts = [];

  @override
  Future<List<HostRecord>> load() async => List.of(hosts);

  @override
  Future<void> save(List<HostRecord> hosts) async =>
      this.hosts = List.of(hosts);
}
