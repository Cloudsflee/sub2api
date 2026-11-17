package productselection

import _ "embed"

// Research is a dated, immutable analysis, not a live price feed or a list of
// approved purchases. It is served only by the authenticated admin endpoint.
//
//go:embed research-20261117.json
var Research []byte
