package api

import _ "embed"

// openAPI is the hand-maintained specification served at /api/v1/openapi.yaml.
// openapi_test.go checks that its paths match the router.
//
//go:embed openapi.yaml
var openAPI []byte
