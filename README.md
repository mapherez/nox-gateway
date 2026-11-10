# NoX Gateway

One connection to the tools in your NoX Suite.

NoX Gateway lets MCP clients and NoX CLI discover and use tools provided by connected Suite applications. Instead of configuring every product in every client, you connect to Gateway and receive the tools your account is authorized to use.

Gateway is a headless service. It has no dashboard and does not implement the applications' business operations: it discovers their definitions, checks current authority and routes each request to the product that owns it.

## What it provides

| Capability | What it does |
| --- | --- |
| A shared MCP endpoint | Gives clients one entry point for connected Suite tools. |
| Product discovery | Reads registered products and their available tool definitions. |
| Authorized catalogs | Presents tools according to the caller's effective access. |
| Request routing | Preserves tool inputs and sends the request to its owning product. |
| Availability tracking | Distinguishes registered products from services that are currently reachable. |
| Failure handling | Propagates errors and cancellation without automatically repeating tool calls. |

Products define their own tools and interaction metadata. Gateway preserves those definitions without adding product-specific menus, commands or storage logic.

## How it fits into NoX Suite

Backend is the authority for personal API keys, registration and service permissions. Yard manages deployment and initiates registration. Gateway consumes that registry and provides the client-facing tool connection.

Participating services communicate on the same host through the shared local channel. Client access is separate: NoX CLI can connect through the installation's private network or VPN. Native HTTP or HTTPS access is a deployment choice, not a requirement to copy certificates between Suite containers.

A client authenticates with a Backend personal API key. Products receive scoped delegations rather than that personal key. Existing valid authority has bounded reuse; an outage does not create new permissions or extend expired authority.

## Documentation

- [Same-host communication and enrollment](https://github.com/mapherez/nox-mcp/blob/master/Documents/SUITE_SAME_HOST_INTEGRATION.md)
- [Gateway and shared integration contract](https://github.com/mapherez/nox-mcp/blob/master/Documents/GATEWAY_CONTRACT.md)
- [Product integration](https://github.com/mapherez/nox-mcp/blob/master/Documents/PRODUCT_INTEGRATION_PROPOSAL.md)
- [SDK integration](https://github.com/mapherez/nox-mcp/blob/master/Documents/INTEGRATION.md)
- [Optional native HTTPS](docs/https-setup.md)

## Usage

Deploy [docker-compose.yml](docker-compose.yml) on the same Docker host as Backend and the participating products. Review [.env.example](.env.example) for the available address, binding, storage and runtime settings.

```sh
docker compose pull
docker compose up -d
```

Use the same local-channel volume name as the other Suite services. In Backend's **Service connections**, enter Gateway's native address, verify its identity and confirm the connection. Pairing and credential delivery run through the local channel; interrupted setup can be resumed from Backend.

For a conventional MCP client, configure your Gateway address followed by `/mcp` and supply your Backend personal API key in the authorization header. In NoX CLI, enter the Gateway base address and key in Settings.

`/healthz` reports process liveness; `/readyz` reports whether Gateway has verified its Backend connection and synchronized the registry. A healthy process alone does not prove that products are ready to execute tools.

Keep Gateway's private data volume when updating and back it up consistently. It contains persistent service identity and trust state. Do not share that data volume as the common runtime channel.

## License & Attribution

This project is licensed under the **Creative Commons Attribution-NonCommercial-ShareAlike 4.0 International (CC BY-NC-SA 4.0)** license.

### How to Credit This Project

If you use, fork, or modify this code, you **must** provide attribution by linking back to my GitHub profile. Please include the following credit notice prominently in your repository or project documentation:

> **Based on [nox-gateway](https://github.com/mapherez/nox-gateway) by [mapherez](https://github.com/mapherez)**
> This project includes code from [nox-gateway](https://github.com/mapherez/nox-gateway), used under the CC BY-NC-SA 4.0 license.

---

*For more details on what you can and cannot do, please refer to the [LICENSE](LICENSE) file in this repository.*

## Disclaimer of Warranty

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
