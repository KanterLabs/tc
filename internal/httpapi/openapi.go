package httpapi

import _ "embed"

// openapi.json is generated from the repository's human-edited openapi.yaml.
// Keeping the complete contract in the binary lets agents generate clients
// without access to the source checkout.
//
//go:embed openapi.json
var openAPIDocument []byte
