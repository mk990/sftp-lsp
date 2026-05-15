package lsp

import "encoding/json"

// ---- JSON-RPC 2.0 base types ----

type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // number, string, or null
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *ResponseError) Error() string { return e.Message }

const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// ---- LSP lifecycle ----

type InitializeParams struct {
	ProcessID    *int               `json:"processId"`
	RootURI      string             `json:"rootUri"`
	RootPath     string             `json:"rootPath"`
	Capabilities ClientCapabilities `json:"capabilities"`
}

type ClientCapabilities struct{}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   ServerInfo         `json:"serverInfo"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ServerCapabilities struct {
	TextDocumentSync       *TextDocumentSyncOptions `json:"textDocumentSync,omitempty"`
	ExecuteCommandProvider *ExecuteCommandOptions   `json:"executeCommandProvider,omitempty"`
	WorkspaceProvider      *WorkspaceCapabilities   `json:"workspace,omitempty"`
}

type TextDocumentSyncOptions struct {
	OpenClose bool `json:"openClose"`
	Save      *SaveOptions `json:"save,omitempty"`
}

type SaveOptions struct {
	IncludeText bool `json:"includeText"`
}

type ExecuteCommandOptions struct {
	Commands []string `json:"commands"`
}

type WorkspaceCapabilities struct {
	WorkspaceFolders *WorkspaceFoldersCapability `json:"workspaceFolders,omitempty"`
}

type WorkspaceFoldersCapability struct {
	Supported           bool `json:"supported"`
	ChangeNotifications bool `json:"changeNotifications"`
}

// ---- workspace/executeCommand ----

type ExecuteCommandParams struct {
	Command   string        `json:"command"`
	Arguments []interface{} `json:"arguments"`
}

// ---- textDocument/didSave ----

type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// ---- window/showMessage ----

type ShowMessageParams struct {
	Type    MessageType `json:"type"`
	Message string      `json:"message"`
}

type MessageType int

const (
	MessageError   MessageType = 1
	MessageWarning MessageType = 2
	MessageInfo    MessageType = 3
	MessageLog     MessageType = 4
)

// ---- Progress ($/progress) ----

type WorkDoneProgressCreateParams struct {
	Token ProgressToken `json:"token"`
}

type ProgressToken = interface{} // string or number

type ProgressParams struct {
	Token ProgressToken `json:"token"`
	Value interface{}   `json:"value"`
}

type WorkDoneProgressBegin struct {
	Kind        string  `json:"kind"` // "begin"
	Title       string  `json:"title"`
	Cancellable bool    `json:"cancellable"`
	Message     string  `json:"message,omitempty"`
	Percentage  *int    `json:"percentage,omitempty"`
}

type WorkDoneProgressReport struct {
	Kind       string `json:"kind"` // "report"
	Message    string `json:"message,omitempty"`
	Percentage *int   `json:"percentage,omitempty"`
}

type WorkDoneProgressEnd struct {
	Kind    string `json:"kind"` // "end"
	Message string `json:"message,omitempty"`
}

// ---- Custom sftp/* notification types ----

// SftpCommandArgs is the common parameter shape for sftp.* executeCommand calls.
type SftpCommandArgs struct {
	LocalPath  string `json:"localPath"`
	RemotePath string `json:"remotePath"`
	ConfigName string `json:"configName"` // selects named profile; empty = first
}

type SftpListResult struct {
	Path    string      `json:"path"`
	Entries []SftpEntry `json:"entries"`
}

type SftpEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"isDir"`
	ModTime string `json:"modTime"` // RFC3339
	Mode    string `json:"mode"`    // e.g. "-rw-r--r--"
}
