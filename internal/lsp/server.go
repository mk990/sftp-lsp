package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Server implements a JSON-RPC 2.0 transport over an io.Reader / io.Writer pair.
// Typically os.Stdin / os.Stdout when used as an LSP server.
type Server struct {
	reader  *bufio.Reader
	writer  io.Writer
	mu      sync.Mutex // guards writer
	handler Handler
	nextID  atomic.Int64
	logger  *log.Logger
}

func NewServer(r io.Reader, w io.Writer, h Handler) *Server {
	return &Server{
		reader:  bufio.NewReaderSize(r, 1<<20),
		writer:  w,
		handler: h,
		logger:  log.New(os.Stderr, "[sftp-lsp] ", log.LstdFlags),
	}
}

// Serve reads and dispatches messages until the reader is closed or an
// unrecoverable error occurs.
func (s *Server) Serve() error {
	for {
		msg, err := s.readMessage()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read message: %w", err)
		}
		go s.dispatch(msg)
	}
}

func (s *Server) readMessage() (*Message, error) {
	// Read headers (Content-Length: N\r\n followed by \r\n).
	contentLength := 0
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length: ") {
			n, err := strconv.Atoi(strings.TrimPrefix(line, "Content-Length: "))
			if err != nil {
				return nil, fmt.Errorf("bad Content-Length: %w", err)
			}
			contentLength = n
		}
	}
	if contentLength == 0 {
		return nil, fmt.Errorf("missing or zero Content-Length")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(s.reader, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &msg, nil
}

func (s *Server) dispatch(msg *Message) {
	if msg.Method == "" {
		// Response to a server-initiated request — ignore for now.
		return
	}

	isRequest := msg.ID != nil
	result, err := s.handler.Handle(s, msg)

	if !isRequest {
		// Notification — no response expected.
		return
	}

	if err != nil {
		code := InternalError
		s.sendError(msg.ID, code, err.Error())
		return
	}
	s.sendResult(msg.ID, result)
}

// SendNotification sends a server → client notification (no ID).
func (s *Server) SendNotification(method string, params interface{}) {
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	if err := s.writeMessage(msg); err != nil {
		s.logger.Printf("send notification %s: %v", method, err)
	}
}

// SendRequest sends a server → client request and returns the request ID.
func (s *Server) SendRequest(method string, params interface{}) interface{} {
	id := s.nextID.Add(1)
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := s.writeMessage(msg); err != nil {
		s.logger.Printf("send request %s: %v", method, err)
	}
	return id
}

// ShowMessage sends window/showMessage to the client.
func (s *Server) ShowMessage(t MessageType, message string) {
	s.SendNotification("window/showMessage", ShowMessageParams{Type: t, Message: message})
}

// ReportProgress sends a $/progress notification.
func (s *Server) ReportProgress(token ProgressToken, value interface{}) {
	s.SendNotification("$/progress", ProgressParams{Token: token, Value: value})
}

func (s *Server) sendResult(id json.RawMessage, result interface{}) {
	var raw json.RawMessage
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			s.sendError(id, InternalError, err.Error())
			return
		}
		raw = b
	} else {
		raw = json.RawMessage("null")
	}
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  json.RawMessage(raw),
	}
	if err := s.writeMessage(msg); err != nil {
		s.logger.Printf("send result: %v", err)
	}
}

func (s *Server) sendError(id json.RawMessage, code int, message string) {
	msg := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error": ResponseError{
			Code:    code,
			Message: message,
		},
	}
	if err := s.writeMessage(msg); err != nil {
		s.logger.Printf("send error: %v", err)
	}
}

func (s *Server) writeMessage(v interface{}) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := io.WriteString(s.writer, header); err != nil {
		return err
	}
	_, err = s.writer.Write(body)
	return err
}
