package admin

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/markusbug/Orchestrator/daemon/internal/config"
	"github.com/markusbug/Orchestrator/daemon/internal/core"
	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
)

func TestAdminOverSocket(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	paths, _ := config.DefaultPaths(filepath.Join(dir, "cfg"))
	cfg := config.Defaults(paths.Home)
	cfg.Roots = []string{root}
	cfg.DefaultCommand = "sh"
	c, err := core.Open(context.Background(), paths, cfg, false, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	srv := &Server{Core: c}
	if err := srv.Listen(paths.AdminSocket); err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	cl := NewClient(paths.AdminSocket)

	st, err := cl.Status()
	if err != nil || st.Fingerprint != c.Identity.Fingerprint || st.Sessions != 0 {
		t.Fatalf("%+v %v", st, err)
	}
	p, err := cl.Pair()
	if err != nil || len(p.Code) != 6 || p.FP != st.Fingerprint {
		t.Fatalf("%+v %v", p, err)
	}
	if code, _, ok := c.Codes.Active(); !ok || code != p.Code {
		t.Fatal("pair did not issue the active code")
	}

	s, err := c.CreateSession(context.Background(), protocol.SessionCreate{Cwd: root, Args: []string{"-c", "cat"}})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := cl.Sessions()
	if len(list) != 1 || list[0].ID != s.ID {
		t.Fatalf("%+v", list)
	}
	if err := cl.Hook(s.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	if s.Info().Status != protocol.StatusWaiting {
		t.Fatal("hook over socket did not apply")
	}
	if err := cl.KillSession(s.ID[:8], "KILL"); err != nil {
		t.Fatalf("kill by prefix: %v", err)
	}
	<-s.Done()
	if err := cl.RemoveSession(s.ID[:8]); err != nil {
		t.Fatalf("remove by prefix: %v", err)
	}
	if list, _ := cl.Sessions(); len(list) != 0 {
		t.Fatal("not removed")
	}
	devs, _ := cl.Devices()
	if len(devs) != 0 {
		t.Fatal("unexpected devices")
	}
	if err := cl.RevokeDevice("nope"); err == nil {
		t.Fatal("revoke of unknown device should fail")
	}
	bad := NewClient(filepath.Join(dir, "missing.sock"))
	if _, err := bad.Status(); err != ErrNotRunning {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
}
