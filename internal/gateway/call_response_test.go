package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	noxmcp "github.com/mapherez/nox-mcp/go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiscoverySeparatesMenusFromConventionalTools(t *testing.T) {
	g, _, _ := fixture(t, "sample-one")
	status, value := rpc(t, g, "noxsync_admin", "tools/list", 1, map[string]any{})
	standard := result(t, status, value)
	for _, entry := range standard["tools"].([]any) {
		tool := entry.(map[string]any)
		meta := tool["_meta"].(map[string]any)
		if _, leaked := meta["cli"]; leaked || meta["custom"] != "preserved" || meta["nox/authorization"] == nil || meta["nox/gateway"] == nil {
			t.Fatal("conventional listing changed non-CLI metadata or leaked menus", tool)
		}
	}
	request := func(method string, params noxmcp.JSON) map[string]any {
		t.Helper()
		params["_meta"] = noxmcp.DiscoveryMeta()
		status, value := rpc(t, g, "noxsync_admin", method, 2, params)
		return result(t, status, value)
	}
	catalog := request("nox/tools/catalog", noxmcp.JSON{})
	definitions := request("nox/tools/definitions", noxmcp.JSON{"catalog": catalog["catalog"], "tools": catalog["tools"]})
	if len(definitions["definitions"].([]any)) != len(standard["tools"].([]any)) {
		t.Fatal("different authorized tools in conventional and NoX discovery")
	}
	for _, entry := range definitions["definitions"].([]any) {
		definition := entry.(map[string]any)
		tool := definition["tool"].(map[string]any)
		meta := tool["_meta"].(map[string]any)
		cli := definition["cli"].(map[string]any)
		options := cli["parameters"].(map[string]any)["/selection"].(map[string]any)["options"].(map[string]any)
		if meta["cli"] == nil || cli["navigation"] == nil || options["tool"] != "sample-one__read" {
			t.Fatal("canonical menus or translated tool references lost", definition)
		}
		hash, err := noxmcp.Fingerprint(noxmcp.JSON{"tool": tool, "cli": cli})
		if err != nil || hash != definition["fingerprint"] {
			t.Fatal("canonical fingerprint changed", err, definition)
		}
	}
	status, value = rpc(t, g, "noxsync_admin", "tools/list", 3, map[string]any{})
	if !reflect.DeepEqual(result(t, status, value), standard) {
		t.Fatal("reading rich definitions mutated the conventional listing")
	}
}

func TestGatewayPreservesExactResultAndArgumentNumbers(t *testing.T) {
	g, _, products := fixture(t, "sample-one", "sample-two")
	for product, state := range products {
		state.response.Store(&mcp.CallToolResult{
			StructuredContent: map[string]any{"product": product, "revision": int64(9007199254740993), "nested": []any{int64(-9223372036854775808), uint64(18446744073709551615), json.Number("1.2300e+40")}},
			Meta:              mcp.Meta{"revision": int64(9007199254740993)},
			Content:           []mcp.Content{&mcp.TextContent{Text: "9007199254740993"}},
		})
	}
	for product := range products {
		t.Run(product, func(t *testing.T) {
			t.Parallel()
			status, value := rpc(t, g, "noxsync_admin", "tools/call", 10, map[string]any{"name": product + "__read", "arguments": map[string]any{"revision": json.Number("9007199254740993")}})
			r := result(t, status, value)
			structured := r["structuredContent"].(map[string]any)
			want := []any{json.Number("-9223372036854775808"), json.Number("18446744073709551615"), json.Number("1.2300e+40")}
			if structured["product"] != product || structured["revision"] != json.Number("9007199254740993") || !reflect.DeepEqual(structured["nested"], want) || r["_meta"].(map[string]any)["revision"] != json.Number("9007199254740993") {
				t.Fatal("result numbers rounded or another product response reused", r)
			}
			if r["content"].([]any)[0].(map[string]any)["text"] != "9007199254740993" {
				t.Fatal("ordinary text was changed", r)
			}
			raw := products[product].request.Load().([]byte)
			if !bytes.Contains(raw, []byte(`"revision":9007199254740993`)) {
				t.Fatal("argument rounded on the product hop", string(raw))
			}
		})
	}
}

func TestGatewayFailuresUseOnlyErrorContent(t *testing.T) {
	for _, code := range []string{"STALE_DEFINITION", "FORBIDDEN", "AUTHORIZATION_INVALIDATED", "EXECUTION_OUTCOME_UNCONFIRMED", "PRODUCT_UNREACHABLE", "PAYLOAD_LIMIT", "unexpected"} {
		t.Run(code, func(t *testing.T) {
			r := callFailure(errors.New(code))
			if !r.IsError || r.StructuredContent != nil || len(r.Content) != 1 {
				t.Fatal("invalid Gateway tool error", r)
			}
			var body map[string]any
			if json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &body) != nil || body["retryable"] != false {
				t.Fatal("missing error details", r)
			}
			if code != "unexpected" && body["code"] != code {
				t.Fatal("error code changed", body)
			}
			if code == "EXECUTION_OUTCOME_UNCONFIRMED" && body["details"].(map[string]any)["outcome"] != "unknown" {
				t.Fatal("uncertain execution hidden", body)
			}
		})
	}
	g, _, products := fixture(t, "sample-one")
	products["sample-one"].response.Store(&mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: `{"code":"CONFLICT","message":"Product rejected the action","retryable":false}`}}})
	status, value := rpc(t, g, "noxsync_admin", "tools/call", 1, map[string]any{"name": "sample-one__read", "arguments": map[string]any{}})
	r := result(t, status, value)
	if r["isError"] != true || r["structuredContent"] != nil || !strings.Contains(r["content"].([]any)[0].(map[string]any)["text"].(string), `"code":"CONFLICT"`) {
		t.Fatal("downstream domain error changed", r)
	}
	products["sample-one"].response.Store(nil)
	products["sample-one"].drop.Store(true)
	status, value = rpc(t, g, "noxsync_admin", "tools/call", 2, map[string]any{"name": "sample-one__write", "arguments": map[string]any{}})
	r = result(t, status, value)
	if r["isError"] != true || r["structuredContent"] != nil || products["sample-one"].writes.Load() != 1 || !strings.Contains(r["content"].([]any)[0].(map[string]any)["text"].(string), `"outcome":"unknown"`) {
		t.Fatal("unknown write result lost or operation replayed", r)
	}
}

func TestCallResponseRejectsUnmatchedOrMissingWireResults(t *testing.T) {
	for _, raw := range []string{`{"id":2,"result":{"structuredContent":{"n":9007199254740993}}}`, `{"id":1,"error":{"code":-32603}}`, `{"id":1,"result":null}`, "data: {}\n\n"} {
		wire := &callResponse{}
		wire.receive(json.RawMessage("1"), []byte(raw))
		if wire.restore(&mcp.CallToolResult{}) == nil {
			t.Fatal("unmatched, missing or non-JSON result accepted", raw)
		}
	}
	wire := &callResponse{}
	r := httptest.NewRequest(http.MethodPost, "http://product/mcp", strings.NewReader(`{"id":1,"method":"tools/call"}`))
	if _, err := wire.request(r, 8); err == nil {
		t.Fatal("unbounded request accepted")
	}
	r = httptest.NewRequest(http.MethodPost, "http://product/mcp", strings.NewReader(`{"id":1,"method":"tools/call"}`))
	id, err := wire.request(r, 1024)
	body, _ := io.ReadAll(r.Body)
	if err != nil || string(id) != "1" || string(body) != `{"id":1,"method":"tools/call"}` {
		t.Fatal("request bytes changed", string(body), err)
	}
}
