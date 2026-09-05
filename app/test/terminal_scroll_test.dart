import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:xterm/xterm.dart';

/// The vendored xterm is patched so that a vertical drag reaches the
/// application as mouse wheel reports when it runs in the alternate screen
/// with mouse tracking (Claude Code's fullscreen renderer). Before the patch
/// the empty scrollback scrollable won the drag on iOS and just bounced.
void main() {
  late Terminal terminal;
  late List<String> sent;

  Future<void> pump(WidgetTester tester, List<String> output) async {
    terminal = Terminal(maxLines: 1000);
    sent = [];
    terminal.onOutput = sent.add;
    await tester.pumpWidget(
      MaterialApp(
        home: Scaffold(
          body: SizedBox(
            height: 300,
            child: TerminalView(terminal, hardwareKeyboardOnly: true),
          ),
        ),
      ),
    );
    for (final o in output) {
      terminal.write(o);
    }
    await tester.pump();
  }

  // iOS: BouncingScrollPhysics, which is what hid the bug.
  final ios = TargetPlatformVariant.only(TargetPlatform.iOS);

  testWidgets('drag in the alternate screen reports wheel events', (
    tester,
  ) async {
    // Enter the alternate screen, enable button + SGR mouse tracking.
    await pump(tester, ['\x1b[?1049h\x1b[?1000h\x1b[?1006h', 'hi']);
    expect(terminal.isUsingAltBuffer, isTrue);

    // Drag the finger down: the reader wants to see earlier content.
    final start = tester.getCenter(find.byType(TerminalView));
    await tester.dragFrom(start, const Offset(0, 120));
    await tester.pumpAndSettle();

    final wheelUp = sent.where((s) => s.startsWith('\x1b[<64;'));
    expect(wheelUp, isNotEmpty, reason: 'sent: $sent');
    expect(wheelUp.first, endsWith('M'));
  }, variant: ios);

  testWidgets('drag in the main buffer scrolls the scrollback', (tester) async {
    await pump(tester, List.generate(200, (i) => 'line $i\r\n'));
    expect(terminal.isUsingAltBuffer, isFalse);

    final scrollable = find.byType(Scrollable).first;
    final before = tester.state<ScrollableState>(scrollable).position.pixels;
    expect(before, greaterThan(0), reason: 'starts stuck to the bottom');

    final start = tester.getCenter(find.byType(TerminalView));
    await tester.dragFrom(start, const Offset(0, 120));
    await tester.pumpAndSettle();

    final after = tester.state<ScrollableState>(scrollable).position.pixels;
    expect(after, lessThan(before));
    expect(sent, isEmpty, reason: 'nothing goes to the app');
  }, variant: ios);
}
