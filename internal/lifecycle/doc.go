// Package lifecycle runs a process between start and exit: SignalContext ends
// the run on SIGINT or SIGTERM, and Shutdown stops the components in order
// within one overall timeout.
//
// Import boundaries: standard library only.
package lifecycle
