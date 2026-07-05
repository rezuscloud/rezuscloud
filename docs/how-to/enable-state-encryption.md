# How to enable state encryption

> **Type:** How-to · **Audience:** Operator

## Overview

Enable OpenTofu state encryption so TF state files are encrypted at rest.

## Steps

1. **Generate a passphrase** (min 16 characters):
   ```bash
   openssl rand -base64 32
   ```

2. **Set it in the Helm values:**
   ```yaml
   rezuscloud:
     statePassphrase: "your-generated-passphrase"
   ```
   Or use an existing secret:
   ```yaml
   existingSecret: my-rezuscloud-secret
   # The secret must contain key: state-passphrase
   ```

3. **Upgrade the release:**
   ```bash
   helm upgrade rezuscloud ./chart \
     --set rezuscloud.statePassphrase="your-passphrase" \
     --reuse-values
   ```

## Verification

Check the startup log:
```
slog.Info("state encryption enabled")
```

`tofu state pull` will now decrypt transparently using the passphrase.
