//go:build mihomo_exit

package main

func runProbeNodeEntry(options probeLaunchOptions) error {
	if err := probeProductPlatformError(); err != nil {
		return err
	}
	return runProbeNode(options)
}
