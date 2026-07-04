// Package e2e contains end-to-end acceptance tests that exercise the full
// rezuscloud pipeline against real Talos VMs booted in QEMU.
//
// These tests require:
//   - QEMU + KVM on the host
//   - A Talos ISO (path via REZUSCLOUD_E2E_TALOS_ISO, default /tmp/talos.iso)
//   - OpenTofu on PATH
//   - REZUSCLOUD_E2E_QEMU=1 environment variable (otherwise skipped)
//
// See docs/testing/e2e-qemu.md for the full design.
package e2e
