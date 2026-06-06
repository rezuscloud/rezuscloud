package integration

import "os"

func init() {
	// Keep integration runtime below CI timeout while preserving bcrypt semantics.
	_ = os.Setenv("REZUSCLOUD_BCRYPT_COST", "6")
}
