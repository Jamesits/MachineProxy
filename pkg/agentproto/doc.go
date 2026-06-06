// Package agentproto defines the CBOR wire protocol shared by local
// runners and mproxy-agent. It receives command, stream, file-operation,
// logging, and recording state from remoteexec and the agent, and feeds
// remote backends, workspacefs, pathstub, and cmd/mproxy-agent with
// stable frame and RPC types.
package agentproto
