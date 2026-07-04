# E2E Acceptance Testing with QEMU

> **Status:** Design approved. Framework in place. Full test implementation
> gated behind KVM availability on the CI runner.

## Why

All existing integration tests use `null_resource` (fake TF) or mock providers.
No test exercises the full pipeline against real Talos VMs:

```
declare tenant → tofu apply creates instances → machines appear in store
  → version bump → rolling upgrade via Talos API → re-apply
```

A QEMU-based E2E test validates the entire pipeline against real Talos
infrastructure without cloud costs.

## Design

### Test topology

```
┌─────────────────────────────────────────────────────┐
│ CI Runner (self-hosted, KVM)                        │
│                                                     │
│  ┌──────────────┐     ┌──────────────────────────┐ │
│  │ rezuscloud   │     │ QEMU Talos VM(s)         │ │
│  │ server       │────▶│ (maintenance mode or     │ │
│  │ + tofu       │     │  bootstrapped cluster)   │ │
│  │ + SQLite     │     │                          │ │
│  └──────────────┘     └──────────────────────────┘ │
│       │                        │                   │
│       └──── metal provider ────┘                   │
│         (talos_machine_configuration_apply          │
│          + talosctl upgrade)                        │
└─────────────────────────────────────────────────────┘
```

The **metal provider** is used (not oci/openstack) because it operates against
pre-booted nodes via the Talos API — exactly what QEMU VMs provide. The test:

1. Boots one or more Talos VMs in QEMU (maintenance mode)
2. Starts `rezuscloud` server with tofu available on PATH
3. Creates a tenant with a metal node group pointing at the QEMU VMs
4. Triggers reconciliation (`tofu apply` via the metal provider)
5. Verifies machines appear in the store with correct addresses
6. Bumps the Talos version in the tenant spec
7. Verifies the rolling upgrade fires (pre-apply hook) and machines converge
8. Tears down VMs

### Gating

- **Build tag:** `e2e` — excluded from normal CI (`-short`, `-tags=integration`)
- **Environment:** `REZUSCLOUD_E2E_QEMU=1` — must be set to run the test
- **Binary check:** skips if `qemu-system` is not on PATH or KVM is unavailable
- **CI workflow:** separate `e2e-qemu.yml`, triggered manually (`workflow_dispatch`)
  or on `v*` tags. NOT part of the PR CI gate.

### Prerequisites on the runner

```bash
# Debian/Ubuntu
sudo apt-get install -y qemu-system-x86 qemu-utils ovmf

# Talos kernel + initramfs (pinned version)
curl -fSsL https://github.com/siderolabs/talos/releases/download/v1.12.6/metal-amd64.iso \
  -o /tmp/talos.iso

# KVM access
ls -la /dev/kvm  # must exist
```

### Test lifecycle

```go
//go:build e2e

func TestE2E_QemuUpgradeCycle(t *testing.T) {
    // 1. Skip if QEMU/KVM unavailable
    requireQemu(t)

    // 2. Boot VM(s) in maintenance mode
    vm := bootTalosVM(t, "talos.iso")
    defer vm.Kill()

    // 3. Start rezuscloud server
    srv := startServer(t, vm.Addr())
    defer srv.Close()

    // 4. Create tenant + metal node group
    createTenant(t, srv, "test-cluster", vm.Addr())

    // 5. Wait for reconciliation
    waitForMachines(t, srv, "test-cluster", 1)

    // 6. Bump version
    bumpVersion(t, srv, "test-cluster", "1.12.7")

    // 7. Wait for upgrade convergence
    waitForUpgrade(t, srv, "test-cluster", "1.12.7")

    // 8. Verify store enrichment
    machines := listMachines(t, srv, "test-cluster")
    assert.Equal(t, "1.12.7", machines[0].Status.TalosVersion)
}
```

### CI workflow

```yaml
# .github/workflows/e2e-qemu.yml
name: E2E (QEMU)
on:
  workflow_dispatch: {}
  push:
    tags: ['v*']
jobs:
  e2e:
    runs-on: [self-hosted, Linux, X64]
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
          cache: false
      - name: Install OpenTofu
        uses: opentofu/setup-opentofu@v2
        with:
          tofu_version_file: .opentofu-version
          tofu_wrapper: false
      - name: Boot QEMU E2E test
        env:
          REZUSCLOUD_E2E_QEMU: "1"
          CGO_ENABLED: "1"
        run: go test -v -tags=e2e -run '^TestE2E' ./tests/e2e/...
```

## Current state

The framework is in place (`tests/e2e/`):
- `qemu.go` — QEMU VM lifecycle helpers (boot, kill, wait for API)
- `qemu_test.go` — the test, gated behind `REZUSCLOUD_E2E_QEMU=1`

The full test implementation requires KVM on the runner. When available, set
`REZUSCLOUD_E2E_QEMU=1` and run:

```bash
go test -v -tags=e2e ./tests/e2e/...
```

## Follow-up

- [ ] Enable KVM on the self-hosted runner
- [ ] Pin a Talos ISO version in CI
- [ ] Add multi-node upgrade test (control-plane + worker rolling)
- [ ] Add convergence test (VM appears in store after apply)
