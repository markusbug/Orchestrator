import 'package:flutter_test/flutter_test.dart';
import 'package:xterm/xterm.dart';

/// The vendored xterm is patched so that the styles Claude Code sets and
/// clears do not leak into the rest of the screen. Before the patch the whole
/// terminal view on the phone was underlined, faint and bold.
void main() {
  int attrs(String output, {int index = 0}) {
    final terminal = Terminal(maxLines: 100);
    terminal.write(output);
    return terminal.buffer.lines[0].getAttributes(index);
  }

  test('a private-prefix CSI ending in m is not SGR', () {
    // XTMODKEYS: Claude Code sends this to turn modifyOtherKeys on, and plain
    // `CSI > 4 m` to turn it off again. Read as SGR it is underline + faint.
    expect(attrs('\x1b[>4;2mhi') & CellAttr.underline, 0);
    expect(attrs('\x1b[>4;2mhi') & CellAttr.faint, 0);
    expect(attrs('\x1b[>4mhi') & CellAttr.underline, 0);
  });

  test('SGR 4 still underlines and 24 still clears it', () {
    expect(attrs('\x1b[4mU') & CellAttr.underline, isNot(0));
    expect(attrs('\x1b[4mU\x1b[24mN', index: 1) & CellAttr.underline, 0);
  });

  test('SGR 22 clears bold as well as faint', () {
    // Claude Code closes bold runs with 22, never with 21.
    expect(attrs('\x1b[1mB\x1b[22mN', index: 1) & CellAttr.bold, 0);
    expect(attrs('\x1b[2mF\x1b[22mN', index: 1) & CellAttr.faint, 0);
  });
}
