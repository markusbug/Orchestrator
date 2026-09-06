import 'package:flutter/material.dart';

import '../app.dart';
import '../model/host.dart';
import '../net/host_connection.dart';
import 'pair_screen.dart';
import 'sessions_screen.dart';
import 'settings_screen.dart';
import 'widgets/common.dart';

class HostsScreen extends StatelessWidget {
  const HostsScreen({super.key});

  @override
  Widget build(BuildContext context) {
    final model = AppScope.of(context);
    final hosts = model.hosts;
    return Scaffold(
      appBar: AppBar(
        title: const Text('Orchestrator'),
        actions: [
          IconButton(
            icon: const Icon(Icons.settings_outlined),
            tooltip: 'Settings',
            onPressed: () => Navigator.push(
              context,
              MaterialPageRoute(builder: (_) => const SettingsScreen()),
            ),
          ),
        ],
      ),
      body: hosts.isEmpty
          ? _Empty(onPair: () => _pair(context))
          : ListView.builder(
              padding: const EdgeInsets.symmetric(vertical: 8),
              itemCount: hosts.length,
              itemBuilder: (context, i) => _HostTile(
                host: hosts[i],
                conn: model.connectionFor(hosts[i].id),
              ),
            ),
      floatingActionButton: FloatingActionButton(
        onPressed: () => _pair(context),
        tooltip: 'Pair a host',
        child: const Icon(Icons.qr_code_scanner),
      ),
    );
  }

  Future<void> _pair(BuildContext context) async {
    final rec = await Navigator.push<HostRecord>(
      context,
      MaterialPageRoute(builder: (_) => const PairScreen()),
    );
    if (rec != null && context.mounted) {
      toast(context, 'Paired with ${rec.name}');
    }
  }
}

class _Empty extends StatelessWidget {
  const _Empty({required this.onPair});

  final VoidCallback onPair;

  @override
  Widget build(BuildContext context) {
    final t = Theme.of(context).textTheme;
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(32),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(
              Icons.dns_outlined,
              size: 64,
              color: Theme.of(context).colorScheme.outline,
            ),
            const SizedBox(height: 16),
            Text('No hosts yet', style: t.titleLarge),
            const SizedBox(height: 8),
            Text(
              'On your computer run `orchestrator pair`, then scan the QR code it prints.',
              textAlign: TextAlign.center,
              style: t.bodyMedium,
            ),
            const SizedBox(height: 24),
            FilledButton.icon(
              onPressed: onPair,
              icon: const Icon(Icons.qr_code_scanner),
              label: const Text('Scan QR code'),
            ),
          ],
        ),
      ),
    );
  }
}

class _HostTile extends StatelessWidget {
  const _HostTile({required this.host, required this.conn});

  final HostRecord host;
  final HostConnection conn;

  @override
  Widget build(BuildContext context) {
    final model = AppScope.read(context);
    final running = conn.runningCount;
    final waiting = conn.waitingCount;
    final total = conn.sessions.length;
    final counts = conn.isOnline
        ? [
            if (waiting > 0) '$waiting waiting',
            if (running > 0) '$running running',
            if (waiting == 0 && running == 0)
              '$total session${total == 1 ? '' : 's'}',
          ].join(' · ')
        : connLabel(conn);
    return Card(
      margin: const EdgeInsets.symmetric(horizontal: 12, vertical: 4),
      child: ListTile(
        leading: Dot(connColor(context, conn.state), size: 12),
        title: Text(
          host.name,
          style: const TextStyle(fontWeight: FontWeight.w600),
        ),
        subtitle: Text(
          conn.isOnline ? '$counts\n${connLabel(conn)}' : counts,
          maxLines: 2,
          overflow: TextOverflow.ellipsis,
        ),
        isThreeLine: conn.isOnline,
        trailing: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            CountBadge(waiting),
            const SizedBox(width: 8),
            const Icon(Icons.chevron_right),
          ],
        ),
        onTap: () => Navigator.push(
          context,
          MaterialPageRoute(builder: (_) => SessionsScreen(hostId: host.id)),
        ),
        onLongPress: () async {
          final action = await showModalBottomSheet<String>(
            context: context,
            builder: (ctx) => SafeArea(
              child: Column(
                mainAxisSize: MainAxisSize.min,
                children: [
                  ListTile(
                    leading: const Icon(Icons.refresh),
                    title: const Text('Reconnect'),
                    onTap: () => Navigator.pop(ctx, 'reconnect'),
                  ),
                  ListTile(
                    leading: const Icon(Icons.edit_outlined),
                    title: const Text('Rename'),
                    onTap: () => Navigator.pop(ctx, 'rename'),
                  ),
                  ListTile(
                    leading: const Icon(Icons.delete_outline),
                    title: const Text('Forget host'),
                    onTap: () => Navigator.pop(ctx, 'forget'),
                  ),
                ],
              ),
            ),
          );
          if (!context.mounted) return;
          switch (action) {
            case 'reconnect':
              conn.reconnectNow();
            case 'rename':
              final name = await promptText(
                context,
                title: 'Rename host',
                initial: host.name,
              );
              if (name != null) await model.renameHost(host.id, name);
            case 'forget':
              if (await confirm(
                context,
                title: 'Forget ${host.name}?',
                body: 'The pairing key is deleted. Sessions on the host keep running.',
                action: 'Forget',
                destructive: true,
              )) {
                await model.forgetHost(host.id);
              }
          }
        },
      ),
    );
  }
}
