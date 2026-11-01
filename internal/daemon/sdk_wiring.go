package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/aloysbr/pigtail/internal/daemon/handlers"
	"github.com/aloysbr/pigtail/internal/sdksession"
	"github.com/aloysbr/pigtail/internal/usagerefresh"
)

// SDKConfig is pigtaild's -sdk-host, -sdk-node and -sdk-launch. A nil SDKConfig
// is the option off. Launch opens POST /subjects/{id}/sdk-sessions; without it
// the sessions on disk can be read and answered, none is launched.
type SDKConfig struct {
	Host   string // directory of clients/sdkhost
	Node   string // node binary
	Launch bool
	// Claude overrides the claude binary (tests); empty resolves it on a PATH
	// cleaned of the cmux shim (H8), at start.
	Claude string
	// Start overrides how the host process is started (tests); nil is cmd.Start.
	Start func(*exec.Cmd) error
}

// wireSDK sets svc.SDK from Config.SDK. Called after wireLauncher: the SDK
// launch runs under the launcher's gate. A failure logs one line and leaves
// the routes at 503 SDK_OFF, with the reason.
func (s *Server) wireSDK(svc *handlers.Services) {
	c := s.cfg.SDK
	if c == nil || c.Host == "" || c.Node == "" {
		return
	}
	off := func(format string, a ...any) {
		svc.SDKOffReason = fmt.Sprintf(format, a...)
		log.Printf("sdk sessions off: %s", svc.SDKOffReason)
	}
	claude := c.Claude
	if claude == "" {
		var err error
		if claude, err = usagerefresh.ResolveClaude(usagerefresh.CleanPath(os.Getenv("PATH"), "")); err != nil {
			off("claude not found on PATH: %v", err)
			return
		}
	}
	reg := &sdksession.Registry{Dir: sdksession.DefaultDir()}
	spawn := sdksession.SpawnConfig{
		Node: c.Node, HostDir: c.Host, Claude: claude,
		SocketPath: s.cfg.SocketPath, DefaultSocket: s.cfg.SocketPath == "" || s.cfg.SocketPath == defaultSocketPath(),
	}
	start := c.Start
	if start == nil {
		start = func(cmd *exec.Cmd) error { return cmd.Start() }
	}
	svc.SDK = &handlers.SDKService{
		Registry:      reg,
		LaunchEnabled: c.Launch && s.launcher != nil,
		Prompt:        s.userSettings().LaunchPrompt,
		NewID:         func() string { return "sdk_" + sdkRandHex(8) },
		NewUUID:       newUUID,
		Reserve: func(m sdksession.Meta) (func(), error) {
			rv, err := reg.Reserve(spawn, m)
			if err != nil {
				return nil, err
			}
			return func() {
				if _, err := rv.Start(start); err != nil {
					log.Printf("sdk host of %s: %v", m.ID, err)
				}
			}, nil
		},
	}
	if s.launcher != nil { // a nil *Launcher in the interface would not be nil
		svc.SDK.Gate = s.launcher
	}
}

func sdkRandHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system random source failing is not recoverable
	}
	return hex.EncodeToString(b)
}

// newUUID is a random (version 4) UUID.
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
