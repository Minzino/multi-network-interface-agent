package network

// Options controls how network adapters configure runtime and persistent state.
// Defaults are tuned for stability when attaching multiple interfaces in the same CIDR.
type Options struct {
	EnablePolicyRouting bool // add per-interface rule+table to keep symmetric routing
	RoutingTableBase    int  // base table number; interface index is added on top
	RouteMetric         int  // metric used for per-interface routes
	UseNoprefixroute    bool // avoid auto-connected routes in main table
	SetArpSysctls       bool // apply arp_ignore/arp_announce hardening
	SetLooseRPFilter    bool // set rp_filter=2 on target interface
}

// DefaultOptions returns the recommended defaults for multinic network handling.
func DefaultOptions() Options {
	return Options{
		EnablePolicyRouting: true,
		RoutingTableBase:    100,
		RouteMetric:         100,
		UseNoprefixroute:    true,
		SetArpSysctls:       true,
		SetLooseRPFilter:    true,
	}
}

// normalize fills zero/invalid fields with safe defaults.
func (o Options) normalize() Options {
	if o.RoutingTableBase <= 0 {
		o.RoutingTableBase = 100
	}
	if o.RouteMetric <= 0 {
		o.RouteMetric = 100
	}
	return o
}

// routingTable returns a stable table number for a multinic interface name.
func (o Options) routingTable(name string) int {
	idx := extractInterfaceIndex(name)
	return o.RoutingTableBase + idx
}

// routeMetric returns a metric with per-interface offset to keep ordering stable.
func (o Options) routeMetric(name string) int {
	idx := extractInterfaceIndex(name)
	return o.RouteMetric + idx
}
