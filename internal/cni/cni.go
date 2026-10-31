// Package cni owns neko-box's CNI configuration. In the M1 model, pod
// networking is performed by containerd's CRI plugin driving a CNI conflist
// installed in the node — the kubelet does NOT build veth/netns by hand (that
// would fight containerd). So this package's job is to generate that conflist.
//
// Today it emits a standard ptp + host-local (+ loopback) config over the pod
// CIDR. The eventual neko-owned CNI (the eBPF connect-hook dataplane, D6) slots
// in here by emitting a conflist that names the neko plugin instead.
package cni

import "fmt"

// Conflist returns a CNI 1.0.0 conflist JSON for the given pod CIDR. ptp gives
// each pod a veth into the node, host-local owns IP allocation from the CIDR.
func Conflist(podCIDR string) string {
	return fmt.Sprintf(`{"cniVersion":"1.0.0","name":"neko","plugins":[`+
		`{"type":"ptp","ipMasq":true,"ipam":{"type":"host-local",`+
		`"ranges":[[{"subnet":"%s"}]],"routes":[{"dst":"0.0.0.0/0"}]}},`+
		`{"type":"loopback"}]}`, podCIDR)
}

// ConflistPath is where the node's CRI runtime reads CNI configs from.
const ConflistPath = "/etc/cni/net.d/10-neko.conflist"
