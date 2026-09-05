import 'dart:convert';

import 'package:flutter/material.dart';
import 'package:shared_preferences/shared_preferences.dart';

/// Keys the terminal key bar can show, in user-chosen order.
enum KeyBarItem {
  esc('Esc'),
  tab('Tab'),
  ctrl('Ctrl'),
  up('↑'),
  down('↓'),
  left('←'),
  right('→'),
  slash('/'),
  shiftTab('⇧Tab'),
  paste('Paste'),
  newline('New line'),
  ctrlC('^C'),
  enter('Enter');

  const KeyBarItem(this.label);
  final String label;

  static const defaultOrder = [
    esc,
    tab,
    ctrl,
    up,
    down,
    left,
    right,
    newline,
    slash,
    shiftTab,
    paste,
  ];
}

/// User preferences. Persisted in shared preferences.
class Settings extends ChangeNotifier {
  Settings({SharedPreferences? prefs})
    : _prefs = prefs; // ignore: prefer_initializing_formals

  static const _kFont = 'settings.fontSize';
  static const _kTheme = 'settings.theme';
  static const _kKeyBar = 'settings.keyBar';
  static const _kHaptics = 'settings.haptics';
  static const _kNotify = 'settings.notify';
  static const _kDeviceName = 'settings.deviceName';

  SharedPreferences? _prefs;

  double fontSize = 13;
  ThemeMode themeMode = ThemeMode.system;
  List<KeyBarItem> keyBar = List.of(KeyBarItem.defaultOrder);
  bool haptics = true;
  bool notifyWaiting = true;
  String deviceName = 'iPhone';

  Future<void> load() async {
    _prefs ??= await SharedPreferences.getInstance();
    final p = _prefs!;
    fontSize = p.getDouble(_kFont) ?? fontSize;
    themeMode = ThemeMode.values[p.getInt(_kTheme) ?? ThemeMode.system.index];
    haptics = p.getBool(_kHaptics) ?? true;
    notifyWaiting = p.getBool(_kNotify) ?? true;
    deviceName = p.getString(_kDeviceName) ?? deviceName;
    final kb = p.getString(_kKeyBar);
    if (kb != null) {
      try {
        final names = (jsonDecode(kb) as List).cast<String>();
        final items = <KeyBarItem>[];
        for (final n in names) {
          final it = KeyBarItem.values.where((k) => k.name == n);
          if (it.isNotEmpty) items.add(it.first);
        }
        if (items.isNotEmpty) keyBar = items;
      } catch (_) {}
    }
    notifyListeners();
  }

  Future<void> setFontSize(double v) async {
    fontSize = v.clamp(8, 28);
    notifyListeners();
    await _prefs?.setDouble(_kFont, fontSize);
  }

  Future<void> setThemeMode(ThemeMode m) async {
    themeMode = m;
    notifyListeners();
    await _prefs?.setInt(_kTheme, m.index);
  }

  Future<void> setKeyBar(List<KeyBarItem> items) async {
    keyBar = List.of(items);
    notifyListeners();
    await _prefs?.setString(
      _kKeyBar,
      jsonEncode(items.map((k) => k.name).toList()),
    );
  }

  Future<void> setHaptics(bool v) async {
    haptics = v;
    notifyListeners();
    await _prefs?.setBool(_kHaptics, v);
  }

  Future<void> setNotifyWaiting(bool v) async {
    notifyWaiting = v;
    notifyListeners();
    await _prefs?.setBool(_kNotify, v);
  }

  Future<void> setDeviceName(String v) async {
    deviceName = v.trim().isEmpty ? 'iPhone' : v.trim();
    notifyListeners();
    await _prefs?.setString(_kDeviceName, deviceName);
  }
}
