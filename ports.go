package main

import (
	"fmt"
	"strconv"
	"strings"
)

// parsePortsLine turns one line of port-spec input into one or more
// PortMap entries, so the wizard (and the tunnel editor) can accept
// whichever of these shapes someone actually types, mixed freely and
// comma-separated on one line:
//
//	1232              -> forwards 1232 to 127.0.0.1:1232
//	1232:2323         -> forwards 1232 to 127.0.0.1:2323
//	1232=host:2323    -> forwards 1232 to host:2323 (the original explicit form)
//	1232,2323,3000:4000,8080=1.2.3.4:9000   -> all of the above at once
func parsePortsLine(line string) ([]PortMap, error) {
	var out []PortMap
	for _, part := range strings.Split(line, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pm, err := parsePortSpec(part)
		if err != nil {
			return nil, err
		}
		out = append(out, pm)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no ports found")
	}
	return out, nil
}

func parsePortSpec(spec string) (PortMap, error) {
	if eq := strings.Index(spec, "="); eq >= 0 {
		port := strings.TrimSpace(spec[:eq])
		target := strings.TrimSpace(spec[eq+1:])
		if !validPort(port) || target == "" {
			return PortMap{}, fmt.Errorf("bad port spec %q (want PORT=host:port)", spec)
		}
		// target may list several "|"-separated backends — see
		// backendpool.go — each just has to be non-empty here; the pool
		// itself validates reachability continuously at runtime.
		for _, b := range strings.Split(target, "|") {
			if strings.TrimSpace(b) == "" {
				return PortMap{}, fmt.Errorf("bad port spec %q (empty backend between |'s)", spec)
			}
		}
		return PortMap{Listen: "0.0.0.0:" + port, Target: target}, nil
	}
	if colon := strings.Index(spec, ":"); colon >= 0 {
		port := strings.TrimSpace(spec[:colon])
		targetPort := strings.TrimSpace(spec[colon+1:])
		if !validPort(port) || !validPort(targetPort) {
			return PortMap{}, fmt.Errorf("bad port spec %q (want PORT:TARGET_PORT)", spec)
		}
		return PortMap{Listen: "0.0.0.0:" + port, Target: "127.0.0.1:" + targetPort}, nil
	}
	if !validPort(spec) {
		return PortMap{}, fmt.Errorf("bad port %q", spec)
	}
	return PortMap{Listen: "0.0.0.0:" + spec, Target: "127.0.0.1:" + spec}, nil
}

func validPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && n <= 65535
}

// askPorts collects forwarded-port entries interactively, accepting any of
// parsePortsLine's formats (including several comma-separated on one line),
// across as many lines as the user wants. forceUDP is true for mode "udp"
// (see udpcarrier.go), where every port is UDP-only already and asking
// "also relay UDP" would be redundant; otherwise it asks that once, same as
// always.
func askPorts(forceUDP bool) []PortMap {
	fmt.Println(bold("Which ports should be exposed?"))
	fmt.Println(dim("A bare port (443) means: expose 443 here, and the Kharej box forwards it"))
	fmt.Println(dim("to its own 127.0.0.1:443 — so the real service must listen on that exact"))
	fmt.Println(dim("port there."))
	fmt.Println(dim("If the service is elsewhere, say so: 443=127.0.0.1:2096"))
	fmt.Println(dim("Several backends for one port: 443=127.0.0.1:2096|127.0.0.1:2097"))
	fmt.Println(dim("(separated by |, health-checked continuously, balanced over the live ones)"))
	fmt.Println(dim("Comma-separated for several ports at once. Blank line to finish."))

	var ports []PortMap
	for {
		line := readLine(fmt.Sprintf("ports (%d so far, blank=done): ", len(ports)))
		if line == "" {
			if len(ports) == 0 {
				fmt.Println(red("at least one port is required."))
				continue
			}
			break
		}
		parsed, err := parsePortsLine(line)
		if err != nil {
			fmt.Println(red(err.Error()))
			continue
		}
		ports = append(ports, parsed...)
	}

	relayUDP := forceUDP
	if !forceUDP {
		relayUDP = confirm("also relay UDP on these ports? (e.g. for WireGuard, games)", false)
	}
	if relayUDP {
		for i := range ports {
			ports[i].UDP = true
		}
	}
	return ports
}

// formatPortSpec is parsePortSpec's inverse, used to show an existing
// PortMap back to the user in the same short form they'd type — e.g. when
// editing a tunnel and displaying its current forwarded ports.
func formatPortSpec(pm PortMap) string {
	port := strings.TrimPrefix(pm.Listen, "0.0.0.0:")
	if pm.Target == "127.0.0.1:"+port {
		return port
	}
	if strings.HasPrefix(pm.Target, "127.0.0.1:") {
		return port + ":" + strings.TrimPrefix(pm.Target, "127.0.0.1:")
	}
	return port + "=" + pm.Target
}
