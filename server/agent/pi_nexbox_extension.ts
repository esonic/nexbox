import { Type } from "@earendil-works/pi-ai";
import { defineTool, type ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { readFileSync, unlinkSync } from "node:fs";

type ServerSpec = { key: string; url: string; headers?: Record<string, string> };
type MCPTool = { name: string; description?: string; inputSchema?: Record<string, unknown> };
type Connection = { spec: ServerSpec; sessionID?: string; protocolVersion: string };
const configPath = process.env.NEXBOX_PI_MCP_CONFIG;
const specs: ServerSpec[] = configPath ? JSON.parse(readFileSync(configPath, "utf8")) : [];
if (configPath) unlinkSync(configPath);
delete process.env.NEXBOX_PI_MCP_CONFIG;

function timeout(signal?: AbortSignal, ms = 30_000) {
	return signal ? AbortSignal.any([signal, AbortSignal.timeout(ms)]) : AbortSignal.timeout(ms);
}

async function responseBody(response: Response, id: string) {
	if ((response.headers.get("content-type") || "").includes("text/event-stream")) {
		const reader = response.body?.getReader();
		if (!reader) throw new Error("MCP SSE response has no body");
		const decoder = new TextDecoder(); let buffer = "";
		for (;;) {
			const { value, done } = await reader.read();
			buffer += decoder.decode(value, { stream: !done });
			let boundary: RegExpExecArray | null;
			while ((boundary = /\r?\n\r?\n/.exec(buffer))) {
				const event = buffer.slice(0, boundary.index); buffer = buffer.slice(boundary.index + boundary[0].length);
			const data = event.split("\n").filter((line) => line.startsWith("data:")).map((line) => line.slice(5).trim()).join("\n");
			if (!data) continue;
			const message = JSON.parse(data);
			const values = Array.isArray(message) ? message : [message];
			const found = values.find((item) => String(item.id) === id);
			if (found) { await reader.cancel(); return found; }
		}
			if (done) break;
		}
		throw new Error("MCP SSE response did not contain the request result");
	}
	return response.json();
}

async function request(connection: Connection, method: string, params: unknown, id?: string, signal?: AbortSignal) {
	const headers: Record<string, string> = { "content-type": "application/json", accept: "application/json, text/event-stream", "mcp-protocol-version": connection.protocolVersion, ...connection.spec.headers };
	if (connection.sessionID) headers["mcp-session-id"] = connection.sessionID;
	const response = await fetch(connection.spec.url, { method: "POST", headers, signal: timeout(signal), body: JSON.stringify({ jsonrpc: "2.0", ...(id ? { id } : {}), method, params }) });
	if (!response.ok) throw new Error(`${connection.spec.key}: MCP ${method} failed (${response.status})`);
	const sid = response.headers.get("mcp-session-id");
	if (sid) connection.sessionID = sid;
	if (!id) return undefined;
	const data = await responseBody(response, id) as { result?: unknown; error?: { message?: string } };
	if (data.error) throw new Error(data.error.message || `${connection.spec.key}: MCP error`);
	return data.result;
}

async function connect(spec: ServerSpec) {
	const connection: Connection = { spec, protocolVersion: "2025-03-26" };
	const initialized = await request(connection, "initialize", { protocolVersion: connection.protocolVersion, capabilities: {}, clientInfo: { name: "nexbox-pi", version: "1.0.0" } }, "init") as { protocolVersion?: string };
	if (initialized.protocolVersion !== connection.protocolVersion) throw new Error(`${spec.key}: unsupported MCP protocol version ${initialized.protocolVersion || "unknown"}`);
	await request(connection, "notifications/initialized", {});
	return connection;
}

function resultText(result: any) {
	return (result?.content || []).filter((block: any) => block.type === "text").map((block: any) => block.text || "").join("\n");
}

export default async function (pi: ExtensionAPI) {
	for (const spec of specs) {
		try {
			const connection = await connect(spec);
			let cursor: string | undefined;
			do {
			const listed = await request(connection, "tools/list", cursor ? { cursor } : {}, `list-${cursor || "first"}`) as { tools?: MCPTool[]; nextCursor?: string };
			cursor = listed.nextCursor;
			for (const tool of listed.tools || []) {
				pi.registerTool(defineTool({
					name: `mcp__${spec.key}__${tool.name}`,
					label: tool.name,
					description: tool.description || `${spec.key} MCP tool: ${tool.name}`,
					parameters: Type.Unsafe(tool.inputSchema || { type: "object", additionalProperties: true }),
					async execute(_id, params, signal) {
						const result = await request(connection, "tools/call", { name: tool.name, arguments: params }, `call-${crypto.randomUUID()}`, signal);
						return { content: [{ type: "text", text: resultText(result) }], details: {}, isError: (result as any)?.isError === true };
					},
				}));
			}
			} while (cursor);
		} catch (error) { console.error(`[nexbox-pi] failed to load MCP ${spec.key}:`, error); }
	}
}
