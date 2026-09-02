package controller

import "sync"

// stripeKeyMu serializes access to stripe-go's package-level API key.  The
// SDK does not expose a per-request key on the legacy session helpers used by
// this project; without a guard, concurrent checkouts (or a hot key rotation)
// can race while one request is reading the key another is replacing it.
var stripeKeyMu sync.Mutex
