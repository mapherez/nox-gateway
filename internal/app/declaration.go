package app

import (
	"encoding/json"

	"github.com/mapherez/nox-gateway/internal/buildinfo"
	noxmcp "github.com/mapherez/nox-mcp/go"
)

// Declaration never advertises the aggregate MCP endpoint as a product source.
func Declaration(version string) (json.RawMessage, error) {
	b, err := json.Marshal(map[string]any{"contractVersion": buildinfo.ContractVersion, "productId": buildinfo.ProductID, "productVersion": version, "displayName": "NoX Gateway", "capabilities": map[string]any{}})
	if err != nil {
		return nil, err
	}
	if _, err = noxmcp.LoadSuiteProductDeclaration(b); err != nil {
		return nil, err
	}
	return b, nil
}
