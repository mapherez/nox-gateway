# NoX Gateway

One connection to the tools in your NoX Suite.

NoX Gateway connects NoX CLI and MCP-compatible AI assistants to the apps in your Suite. Configure one Gateway connection in your client, then discover and use the tools available to your account across connected apps.

- One MCP address instead of a separate connection for every app.
- Automatic discovery of registered products and their tools.
- Access checked against your Backend personal API key.
- Requests routed to the app that performs the operation.

Gateway runs as a background service. It has no dashboard: you connect it from Backend, then use it from CLI or your chosen MCP client.

## What you need

- Docker with Docker Compose.
- NoX Backend and the participating apps on the **same Docker host**.
- A private address reachable by the computers that will use CLI or MCP, either on your LAN or through a VPN.
- A personal API key generated in Backend.

Gateway can use a private HTTP address. You do not need to generate certificates or expose it to the internet to use your Suite.

## Help

[Suite connection reference](https://github.com/mapherez/nox-mcp/blob/master/Documents/SUITE_SAME_HOST_INTEGRATION.md) · [Optional HTTPS](docs/https-setup.md) · [Report a problem](https://github.com/mapherez/nox-gateway/issues)

For integration developers: [Gateway contract](https://github.com/mapherez/nox-mcp/blob/master/Documents/GATEWAY_CONTRACT.md).

## Usage

### Install

On the same Docker server as Backend, download the deployment file into a new folder:

```sh
mkdir nox-gateway
cd nox-gateway
curl -fsSLo docker-compose.yml https://raw.githubusercontent.com/mapherez/nox-gateway/main/docker-compose.yml
```

The supplied configuration uses host port **5711**. If that port is already occupied, create a `.env` file beside the Compose file with `NOX_GATEWAY_PORT` set to an unused port. If your Suite uses a custom local volume name, also set the matching `NOX_SUITE_LOCAL_VOLUME_NAME`. No API key needs to be put in Gateway's environment.

Start Gateway:

```sh
docker compose pull
docker compose up -d
```

### Connect Gateway to Backend

1. Open Backend's dashboard and go to **Service connections**.
2. Enter Gateway's private base address, including the published port.
3. Choose **Verify**, then **Connect**, and confirm the verified local identity.

Use your server's hostname or private address. Pairing happens through the shared local Docker volume; you do not need to copy credential files. Services on other hosts cannot join through that channel.

### Use your apps

In **NoX CLI**, open Settings and enter the Gateway base address and your Backend personal API key.

In an **MCP-compatible AI client**, add a Streamable HTTP connection using the Gateway base address followed by `/mcp`. Set its authorization header to `Authorization: Bearer <YOUR_BACKEND_API_KEY>`, replacing the placeholder with your key. The exact settings screen depends on the client.

Gateway exposes tools from connected, available products. A registered service and a currently available MCP endpoint are separate states.

### Update and check the service

From the installation folder:

```sh
docker compose pull
docker compose up -d
```

Keep Gateway's data volume when updating. For a basic check, open its address followed by `/healthz`. `/readyz` additionally checks whether Gateway has verified its Backend connection and synchronized the registry.

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
