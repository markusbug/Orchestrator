package vconn

import (
	"context"
	"io"
	"net"

	"github.com/coder/websocket"
)

// FromWebSocket adapts a WebSocket carrying binary messages into a net.Conn
// with real deadline semantics.
//
// websocket.NetConn is not suitable underneath net/http: on Hijack the HTTP
// server aborts its background read by setting a deadline in the past, and
// NetConn implements an expired deadline during an in-flight read by
// cancelling its read context for good, which closes the WebSocket. Here the
// returned conn is one end of a net.Pipe (whose deadlines behave like a
// socket's) and two goroutines shuttle bytes to and from the WebSocket. The
// pipe is unbuffered, so TCP flow control still reaches end to end.
//
// Closing the returned conn closes the WebSocket normally; the WebSocket
// closing (or ctx ending) makes the returned conn report EOF.
func FromWebSocket(ctx context.Context, ws *websocket.Conn) net.Conn {
	ours, theirs := net.Pipe()
	ws.SetReadLimit(1 << 20)
	go func() { // websocket -> pipe
		defer theirs.Close()
		for {
			_, r, err := ws.Reader(ctx)
			if err != nil {
				return
			}
			if _, err := io.Copy(theirs, r); err != nil {
				// Our side was closed; the other goroutine closes the socket.
				return
			}
		}
	}()
	go func() { // pipe -> websocket
		buf := make([]byte, 32<<10)
		for {
			n, err := theirs.Read(buf)
			if n > 0 {
				if werr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					theirs.Close()
					return
				}
			}
			if err != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "")
				return
			}
		}
	}()
	return ours
}
