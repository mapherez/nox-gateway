// Package buildinfo supplies product metadata without inventing a release.
package buildinfo

import noxmcp "github.com/mapherez/nox-mcp/go"

const ProductID = "nox-gateway"
const ContractVersion = noxmcp.SuiteContractVersion

// Version is overridden at build time with -X for an actual tagged release.
var Version = "dev"
