package discovery

// maxLANAddresses is how many Hello.lan_addresses carries
const maxLANAddresses = 4

// LANAddresses are the device's IPv4 addresses on its local networks (no loopback, link-local or point-to-point
// tunnel), at most 4, in interface order: where a multiroom group's members reach it when it leads
func LANAddresses() []string {
	var out []string
	for _, ifc := range multicastIfaces() {
		for _, a := range ifc.addrs {
			if len(out) == maxLANAddresses {
				return out
			}
			out = append(out, a.String())
		}
	}
	return out
}
