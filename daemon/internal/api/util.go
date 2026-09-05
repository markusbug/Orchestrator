package api

import (
	"encoding/base64"

	"github.com/markusbug/Orchestrator/daemon/internal/core"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func coreVersion() string { return core.Version }
