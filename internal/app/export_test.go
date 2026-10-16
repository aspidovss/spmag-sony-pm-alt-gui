package app

import "github.com/aspidovss/spmag-sony-pm-alt-gui/internal/ssdp"

// InjectDiscovery simulates an SSDP announcement (tests only).
func InjectDiscovery(c *Controller, cam ssdp.Camera) {
	c.cmds <- command{kind: cmdDiscovered, cam: cam}
}
