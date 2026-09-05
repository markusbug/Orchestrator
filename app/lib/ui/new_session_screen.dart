import 'dart:async';

import 'package:flutter/material.dart';

import '../app.dart';
import '../model/fs.dart';
import '../net/host_connection.dart';
import 'command_sheet.dart';
import 'terminal_screen.dart';
import 'widgets/common.dart';

/// Folder picker: recents, then a breadcrumb browser with search.
class NewSessionScreen extends StatefulWidget {
  const NewSessionScreen({super.key, required this.hostId});

  final String hostId;

  @override
  State<NewSessionScreen> createState() => _NewSessionScreenState();
}

class _NewSessionScreenState extends State<NewSessionScreen> {
  HostConnection get conn =>
      AppScope.read(context).connectionFor(widget.hostId);

  final _search = TextEditingController();
  Timer? _debounce;
  List<String> _recents = const [];
  FsListing? _listing;
  List<FsEntry>? _results;
  bool _hidden = false;
  bool _loading = true;
  String? _error;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addPostFrameCallback((_) => _load());
  }

  @override
  void dispose() {
    _debounce?.cancel();
    _search.dispose();
    super.dispose();
  }

  Future<void> _load() async {
    final c = conn;
    setState(() {
      _loading = true;
      _error = null;
    });
    try {
      final home = c.home ?? '/';
      final results = await Future.wait([
        c.fsRecents(),
        c.fsList(home, hidden: _hidden),
      ]);
      if (!mounted) return;
      setState(() {
        _recents = results[0] as List<String>;
        _listing = results[1] as FsListing;
        _loading = false;
      });
    } catch (e) {
      if (!mounted) return;
      setState(() {
        _error = e.toString();
        _loading = false;
      });
    }
  }

  Future<void> _goTo(String path) async {
    try {
      final l = await conn.fsList(path, hidden: _hidden);
      if (!mounted) return;
      setState(() {
        _listing = l;
        _results = null;
        _search.clear();
      });
    } catch (e) {
      if (mounted) toast(context, '$e');
    }
  }

  void _onSearchChanged(String q) {
    _debounce?.cancel();
    if (q.trim().isEmpty) {
      setState(() => _results = null);
      return;
    }
    _debounce = Timer(const Duration(milliseconds: 300), () async {
      try {
        final r = await conn.fsSearch(q.trim());
        if (mounted && _search.text.trim() == q.trim()) {
          setState(() => _results = r.where((e) => e.dir).toList());
        }
      } catch (e) {
        if (mounted) toast(context, '$e');
      }
    });
  }

  Future<void> _choose(String cwd) async {
    final c = conn;
    final spec = await showModalBottomSheet<SessionSpec>(
      context: context,
      isScrollControlled: true,
      builder: (_) => CommandSheet(conn: c, cwd: cwd),
    );
    if (spec == null || !mounted) return;
    try {
      final size = TerminalScreen.guessSize(
        context,
        AppScope.read(context).settings.fontSize,
      );
      final s = await c.createSession(
        cwd: cwd,
        cmd: spec.cmd,
        args: spec.args,
        name: spec.name,
        cols: size.$1,
        rows: size.$2,
      );
      if (!mounted) return;
      Navigator.pushReplacement(
        context,
        MaterialPageRoute(
          builder: (_) =>
              TerminalScreen(hostId: widget.hostId, sessionId: s.id),
        ),
      );
    } catch (e) {
      if (mounted) toast(context, 'Could not start: $e');
    }
  }

  @override
  Widget build(BuildContext context) {
    final home = conn.home;
    final listing = _listing;
    return Scaffold(
      appBar: AppBar(
        title: const Text('New session'),
        actions: [
          IconButton(
            icon: Icon(
              _hidden ? Icons.visibility : Icons.visibility_off_outlined,
            ),
            tooltip: 'Show hidden folders',
            onPressed: () {
              setState(() => _hidden = !_hidden);
              if (listing != null) _goTo(listing.path);
            },
          ),
        ],
        bottom: PreferredSize(
          preferredSize: const Size.fromHeight(56),
          child: Padding(
            padding: const EdgeInsets.fromLTRB(12, 0, 12, 8),
            child: TextField(
              controller: _search,
              onChanged: _onSearchChanged,
              autocorrect: false,
              decoration: InputDecoration(
                hintText: 'Search folders',
                prefixIcon: const Icon(Icons.search),
                suffixIcon: _search.text.isEmpty
                    ? null
                    : IconButton(
                        icon: const Icon(Icons.clear),
                        onPressed: () =>
                            _onSearchChanged((_search..clear()).text),
                      ),
                isDense: true,
                border: const OutlineInputBorder(
                  borderRadius: BorderRadius.all(Radius.circular(12)),
                ),
              ),
            ),
          ),
        ),
      ),
      body: _loading
          ? const Center(child: CircularProgressIndicator())
          : _error != null
          ? Center(
              child: Padding(
                padding: const EdgeInsets.all(24),
                child: Text(_error!),
              ),
            )
          : _results != null
          ? _SearchResults(
              results: _results!,
              home: home,
              onOpen: _goTo,
              onChoose: _choose,
            )
          : _Browser(
              recents: _recents,
              listing: listing,
              home: home,
              onOpen: _goTo,
              onChoose: _choose,
            ),
      bottomNavigationBar: listing == null || _results != null
          ? null
          : SafeArea(
              child: Padding(
                padding: const EdgeInsets.fromLTRB(12, 8, 12, 8),
                child: FilledButton.icon(
                  onPressed: () => _choose(listing.path),
                  icon: const Icon(Icons.play_arrow),
                  label: Text(
                    'Start in ${shortenPath(listing.path, home)}',
                    overflow: TextOverflow.ellipsis,
                  ),
                ),
              ),
            ),
    );
  }
}

class _Browser extends StatelessWidget {
  const _Browser({
    required this.recents,
    required this.listing,
    required this.home,
    required this.onOpen,
    required this.onChoose,
  });

  final List<String> recents;
  final FsListing? listing;
  final String? home;
  final void Function(String path) onOpen;
  final void Function(String path) onChoose;

  @override
  Widget build(BuildContext context) {
    final l = listing;
    final t = Theme.of(context).textTheme;
    final dirs = l?.entries.where((e) => e.dir).toList() ?? const <FsEntry>[];
    return ListView(
      children: [
        if (recents.isNotEmpty) ...[
          Padding(
            padding: const EdgeInsets.fromLTRB(16, 12, 16, 4),
            child: Text('Recent', style: t.labelLarge),
          ),
          for (final p in recents)
            ListTile(
              dense: true,
              leading: const Icon(Icons.history),
              title: Text(
                shortenPath(p, home),
                overflow: TextOverflow.ellipsis,
              ),
              trailing: IconButton(
                icon: const Icon(Icons.folder_open_outlined),
                tooltip: 'Browse',
                onPressed: () => onOpen(p),
              ),
              onTap: () => onChoose(p),
            ),
          const Divider(),
        ],
        if (l != null) ...[
          _Breadcrumbs(path: l.path, home: home, onOpen: onOpen),
          if (dirs.isEmpty)
            Padding(
              padding: const EdgeInsets.all(24),
              child: Text(
                'No subfolders',
                style: t.bodyMedium,
                textAlign: TextAlign.center,
              ),
            ),
          for (final e in dirs)
            ListTile(
              leading: Icon(
                e.git ? Icons.account_tree_outlined : Icons.folder_outlined,
              ),
              title: Text(e.name, overflow: TextOverflow.ellipsis),
              subtitle: e.git ? const Text('git repository') : null,
              trailing: IconButton(
                icon: const Icon(Icons.play_arrow_outlined),
                tooltip: 'Start here',
                onPressed: () => onChoose(e.path),
              ),
              onTap: () => onOpen(e.path),
            ),
        ],
      ],
    );
  }
}

class _Breadcrumbs extends StatelessWidget {
  const _Breadcrumbs({
    required this.path,
    required this.home,
    required this.onOpen,
  });

  final String path;
  final String? home;
  final void Function(String path) onOpen;

  @override
  Widget build(BuildContext context) {
    final parts = path.split('/').where((p) => p.isNotEmpty).toList();
    final crumbs = <(String, String)>[('/', '/')];
    var acc = '';
    for (final p in parts) {
      acc = '$acc/$p';
      crumbs.add((p, acc));
    }
    // Collapse the home prefix into "~".
    final h = home;
    var start = 0;
    if (h != null && (path == h || path.startsWith('$h/'))) {
      final hp = h.split('/').where((p) => p.isNotEmpty).length;
      start = hp;
      crumbs[hp] = ('~', h);
    }
    final shown = crumbs.sublist(start);
    return SingleChildScrollView(
      scrollDirection: Axis.horizontal,
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 6),
      child: Row(
        children: [
          for (var i = 0; i < shown.length; i++) ...[
            if (i > 0) const Icon(Icons.chevron_right, size: 18),
            ActionChip(
              label: Text(shown[i].$1),
              onPressed: i == shown.length - 1
                  ? null
                  : () => onOpen(shown[i].$2),
              visualDensity: VisualDensity.compact,
            ),
          ],
        ],
      ),
    );
  }
}

class _SearchResults extends StatelessWidget {
  const _SearchResults({
    required this.results,
    required this.home,
    required this.onOpen,
    required this.onChoose,
  });

  final List<FsEntry> results;
  final String? home;
  final void Function(String path) onOpen;
  final void Function(String path) onChoose;

  @override
  Widget build(BuildContext context) {
    if (results.isEmpty) {
      return const Center(child: Text('No matching folders'));
    }
    return ListView.builder(
      itemCount: results.length,
      itemBuilder: (context, i) {
        final e = results[i];
        return ListTile(
          leading: Icon(
            e.git ? Icons.account_tree_outlined : Icons.folder_outlined,
          ),
          title: Text(e.name),
          subtitle: Text(
            shortenPath(e.path, home),
            overflow: TextOverflow.ellipsis,
          ),
          trailing: IconButton(
            icon: const Icon(Icons.play_arrow_outlined),
            tooltip: 'Start here',
            onPressed: () => onChoose(e.path),
          ),
          onTap: () => onOpen(e.path),
        );
      },
    );
  }
}
