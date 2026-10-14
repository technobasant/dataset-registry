// Package openapi embeds the API's OpenAPI 3.1 description.
package openapi

import _ "embed"

// Spec is openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
