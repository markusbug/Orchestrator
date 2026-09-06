import 'package:flutter/material.dart';

import '../app.dart';
import '../services/settings.dart';
import 'widgets/common.dart';

class SettingsScreen extends StatelessWidget {
  const SettingsScreen({super.key});

  @override
  Widget build(BuildContext context) {
    final model = AppScope.of(context);
    final s = model.settings;
    return ListenableBuilder(
      listenable: s,
      builder: (context, _) => Scaffold(
        appBar: AppBar(title: const Text('Settings')),
        body: ListView(
          children: [
            _Header('Terminal'),
            ListTile(
              title: const Text('Font size'),
              subtitle: Slider(
                value: s.fontSize,
                min: 8,
                max: 28,
                divisions: 20,
                label: s.fontSize.toStringAsFixed(0),
                onChanged: (v) => s.setFontSize(v),
              ),
              trailing: Text(s.fontSize.toStringAsFixed(0)),
            ),
            ListTile(
              title: const Text('Theme'),
              trailing: SegmentedButton<ThemeMode>(
                segments: const [
                  ButtonSegment(value: ThemeMode.system, label: Text('Auto')),
                  ButtonSegment(value: ThemeMode.light, label: Text('Light')),
                  ButtonSegment(value: ThemeMode.dark, label: Text('Dark')),
                ],
                selected: {s.themeMode},
                onSelectionChanged: (v) => s.setThemeMode(v.first),
              ),
            ),
            ListTile(
              title: const Text('Key bar'),
              subtitle: Text(s.keyBar.map((k) => k.label).join('  ')),
              trailing: const Icon(Icons.chevron_right),
              onTap: () => Navigator.push(
                context,
                MaterialPageRoute(builder: (_) => const KeyBarScreen()),
              ),
            ),
            _Header('Feedback'),
            SwitchListTile(
              title: const Text('Haptics'),
              subtitle: const Text('Vibrate when a session starts waiting'),
              value: s.haptics,
              onChanged: s.setHaptics,
            ),
            SwitchListTile(
              title: const Text('Notifications'),
              subtitle: const Text(
                'Notify while in the background and still connected',
              ),
              value: s.notifyWaiting,
              onChanged: s.setNotifyWaiting,
            ),
            _Header('This phone'),
            ListTile(
              title: const Text('Device name'),
              subtitle: Text(s.deviceName),
              trailing: const Icon(Icons.edit_outlined),
              onTap: () async {
                final v = await promptText(
                  context,
                  title: 'Device name',
                  initial: s.deviceName,
                );
                if (v != null) s.setDeviceName(v);
              },
            ),
            _Header('Hosts'),
            if (model.hosts.isEmpty)
              const ListTile(title: Text('No paired hosts')),
            for (final h in model.hosts)
              ListTile(
                title: Text(h.name),
                subtitle: Text(
                  '${h.hostname} · ${h.addrs.map((a) => a.isRelay ? 'relay' : '${a.ip}:${a.portOr(h.port)}').join(', ')}\n${h.fingerprint}',
                  style: const TextStyle(fontSize: 11),
                ),
                isThreeLine: true,
                trailing: PopupMenuButton<String>(
                  onSelected: (v) async {
                    if (v == 'rename') {
                      final name = await promptText(
                        context,
                        title: 'Rename host',
                        initial: h.name,
                      );
                      if (name != null) await model.renameHost(h.id, name);
                    } else if (v == 'forget') {
                      if (!context.mounted) return;
                      if (await confirm(
                        context,
                        title: 'Forget ${h.name}?',
                        body: 'The pairing key is deleted. Sessions on the host keep running.',
                        action: 'Forget',
                        destructive: true,
                      )) {
                        await model.forgetHost(h.id);
                      }
                    }
                  },
                  itemBuilder: (_) => const [
                    PopupMenuItem(value: 'rename', child: Text('Rename')),
                    PopupMenuItem(value: 'forget', child: Text('Forget')),
                  ],
                ),
              ),
            const SizedBox(height: 24),
          ],
        ),
      ),
    );
  }
}

class _Header extends StatelessWidget {
  const _Header(this.text);

  final String text;

  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.fromLTRB(16, 20, 16, 4),
    child: Text(
      text,
      style: Theme.of(context).textTheme.labelLarge
          ?.copyWith(color: Theme.of(context).colorScheme.primary),
    ),
  );
}

/// Reorder and toggle the keys shown above the keyboard.
class KeyBarScreen extends StatefulWidget {
  const KeyBarScreen({super.key});

  @override
  State<KeyBarScreen> createState() => _KeyBarScreenState();
}

class _KeyBarScreenState extends State<KeyBarScreen> {
  late List<KeyBarItem> _enabled;

  @override
  void initState() {
    super.initState();
    _enabled = List.of(AppScope.read(context).settings.keyBar);
  }

  void _save() => AppScope.read(context).settings.setKeyBar(_enabled);

  @override
  Widget build(BuildContext context) {
    final disabled = KeyBarItem.values
        .where((k) => !_enabled.contains(k))
        .toList();
    return Scaffold(
      appBar: AppBar(
        title: const Text('Key bar'),
        actions: [
          TextButton(
            onPressed: () => setState(() {
              _enabled = List.of(KeyBarItem.defaultOrder);
              _save();
            }),
            child: const Text('Reset'),
          ),
        ],
      ),
      body: Column(
        children: [
          Expanded(
            child: ReorderableListView(
              header: const Padding(
                padding: EdgeInsets.all(16),
                child: Text('Drag to reorder. Swipe to remove.'),
              ),
              onReorderItem: (a, b) => setState(() {
                final k = _enabled.removeAt(a);
                _enabled.insert(b, k);
                _save();
              }),
              children: [
                for (final k in _enabled)
                  Dismissible(
                    key: ValueKey(k),
                    onDismissed: (_) => setState(() {
                      _enabled.remove(k);
                      _save();
                    }),
                    child: ListTile(
                      title: Text(k.label),
                      subtitle: Text(k.name),
                      trailing: const Icon(Icons.drag_handle),
                    ),
                  ),
              ],
            ),
          ),
          if (disabled.isNotEmpty)
            SafeArea(
              child: Padding(
                padding: const EdgeInsets.all(12),
                child: Wrap(
                  spacing: 8,
                  children: [
                    for (final k in disabled)
                      ActionChip(
                        avatar: const Icon(Icons.add, size: 16),
                        label: Text(k.label),
                        onPressed: () => setState(() {
                          _enabled.add(k);
                          _save();
                        }),
                      ),
                  ],
                ),
              ),
            ),
        ],
      ),
    );
  }
}
