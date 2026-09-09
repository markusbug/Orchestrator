package termscan

import "testing"

func TestBellIgnoresControlStrings(t *testing.T) {
	var b Bell
	cases := []struct {
		in   string
		want int
	}{
		{"plain\x07", 1},
		{"\x1b]0;\xe2\x97\x90 Pong response\x07", 0}, // Claude Code title update
		{"\x1b]0;title\x1b\\", 0},                    // OSC with ST
		{"\x1bP!|00000000\x1b\\\x07", 1},             // DCS then a real bell
		{"\x1b[31m\x07", 1},                          // CSI then bell
		{"\x1b_apc\x07\x07", 1},                      // APC ends at first BEL
		{"\x07\x07", 2},
		{"\x1b\x07", 1}, // ESC followed by BEL: not a sequence
	}
	for _, c := range cases {
		if got := b.Feed([]byte(c.in)); got != c.want {
			t.Errorf("%q: got %d bells, want %d", c.in, got, c.want)
		}
	}
}

func TestBellAcrossChunks(t *testing.T) {
	var b Bell
	n := b.Feed([]byte("\x1b]0;half a title"))
	n += b.Feed([]byte(" and the rest\x07"))
	if n != 0 {
		t.Fatalf("split OSC counted %d bells", n)
	}
	n = b.Feed([]byte("\x1b"))
	n += b.Feed([]byte("]2;x\x07"))
	if n != 0 {
		t.Fatalf("split ESC counted %d bells", n)
	}
	n = b.Feed([]byte("\x1b]2;x\x1b"))
	n += b.Feed([]byte("\\\x07"))
	if n != 1 {
		t.Fatalf("split ST: got %d, want 1", n)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		in   string
		want InputKind
	}{
		{"", Report},
		{"\x1b[?1;2c", Report},            // DA1 reply
		{"\x1b[>0;0;0c", Report},          // DA2 reply
		{"\x1bP!|00000000\x1b\\", Report}, // DA3 reply
		{"\x1b[0n", Report},               // operating status
		{"\x1b[12;40R", Report},           // cursor position
		{"\x1b[8;40;110t", Report},        // size report
		{"\x1b[I", Report},                // focus in
		{"\x1b[O", Report},                // focus out
		{"\x1b[<64;10;5M", Report},        // SGR mouse wheel
		{"\x1b[<64;10;5M\x1b[<64;10;5M", Report},
		{"\x1b[M !!", Report}, // X10 mouse
		{"\x1b", Interrupt},
		{"\x03", Interrupt},
		{"\r", Answer},
		{"\n", Answer},
		{"y", Answer},
		{"2", Answer},
		{"hello", Keys},
		{"hello\r", Answer},
		{"\x1b[A", Keys},    // arrow up
		{"\x1b[1;5C", Keys}, // ctrl-right
		{"\x1b[15~", Keys},  // F5
		{"\x1bOA", Keys},    // SS3 arrow
		{"\x1bx", Keys},     // alt-x
		{"\x7f", Keys},      // backspace
		{"\t", Keys},
		{"\x1b[200~pasted\r\ntext\x1b[201~", Keys},
		{"\x1b[?1;2c\r", Answer}, // reply followed by Enter
	}
	for _, c := range cases {
		if got := Classify([]byte(c.in)); got != c.want {
			t.Errorf("%q: got %v, want %v", c.in, got, c.want)
		}
	}
}
