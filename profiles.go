package main

import (
	"errors"
	"log"
	"time"
)

// Why there is no "servers agree to switch" handshake
//
// The obvious design for "if a transport is having trouble, switch" is a
// control message: one end notices, tells the other, both change over
// together. That has a bootstrapping problem here: the message asking to
// switch has to cross the same network path that is already having trouble,
// on the same connection that may be the thing about to be reset. A
// coordination channel is just one more thing a censor can also block, and
// now failing over depends on it working.
//
// So instead: the server listens on every enabled disguise at once, all the
// time — see server.go's multi-listener Run. There is nothing to switch on
// the server side; whichever profile the client tries next is already live.
// All the intelligence is client-side, in profileState below: each profile
// remembers its own recent luck, a profile that just failed sits out a
// cooldown instead of being retried immediately, and a profile that failed
// fast (see shortLivedThreshold) is treated as more suspicious — likely
// active interference rather than an ordinary blip — and cools down harder.
// The moment the client dials a different profile, the switch has already
// happened, on both ends, because the server was already there.

// errDegraded is returned by Client.runOnce when the connection did not
// die, but its measured RTT crossed degradedRTTMs for degradedStreak
// consecutive pings — the client's own signal, without waiting for a hard
// failure, that this profile is not a good one to keep using right now.
var errDegraded = errors.New("profile degraded (sustained high RTT)")

const (
	// shortLivedThreshold: a control channel that dies before living this
	// long reads as interference, not an ordinary disconnect — this is
	// mostly the "did a censor reset this connection almost immediately"
	// signal.
	shortLivedThreshold = 20 * time.Second

	degradedRTTMs  = 4000
	degradedStreak = 3
)

// profileState tracks one disguise candidate's recent luck.
type profileState struct {
	cfg           *DisguiseConfig
	failCount     int
	cooldownUntil time.Time
}

func (p *profileState) label() string { return p.cfg.Type + " " + p.cfg.ServerAddr }

func buildProfiles(cfg *Config) []*profileState {
	var out []*profileState
	for i := range cfg.Disguise {
		d := &cfg.Disguise[i]
		if d.Enabled {
			out = append(out, &profileState{cfg: d})
		}
	}
	return out
}

// pickProfile returns the most-preferred profile not currently cooling
// down, or the single most-preferred one if every candidate is — trying the
// best option is still better than trying nothing.
func pickProfile(profiles []*profileState) *profileState {
	now := time.Now()
	for _, p := range profiles {
		if now.After(p.cooldownUntil) {
			return p
		}
	}
	return profiles[0]
}

func (p *profileState) onFailure(escalate bool) {
	if escalate {
		p.failCount += 3
	} else {
		p.failCount++
	}
	d := time.Duration(p.failCount) * 5 * time.Second
	if d > 3*time.Minute {
		d = 3 * time.Minute
	}
	p.cooldownUntil = time.Now().Add(d)
	log.Printf("profile %s: unhealthy, cooling down for %s", p.label(), d.Round(time.Second))
}

func (p *profileState) onSuccess() {
	if p.failCount > 0 {
		log.Printf("profile %s: recovered", p.label())
	}
	p.failCount = 0
	p.cooldownUntil = time.Time{}
}
