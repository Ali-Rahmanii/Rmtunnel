package main

import "github.com/xtaci/smux"

// muxConfig builds the smux session config from the tunable knobs in Config.
// Version is fixed at 2 (the more efficient framing) rather than negotiated:
// this project has no legacy peer to stay compatible with, so both ends
// always run the same code and the negotiation ladder BackPack needs for
// mixed-version fleets simply does not apply here.
func muxConfig(cfg *Config) *smux.Config {
	c := smux.DefaultConfig()
	c.Version = 2
	c.KeepAliveInterval = cfg.MuxKeepAlive.Duration
	c.KeepAliveTimeout = 3 * cfg.MuxKeepAlive.Duration
	c.MaxFrameSize = cfg.MuxFrameSize
	c.MaxReceiveBuffer = cfg.MuxRecvBuffer
	c.MaxStreamBuffer = cfg.MuxStreamBuffer
	return c
}
