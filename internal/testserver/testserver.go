// Package testserver serves a scripted OpenAI-compatible chat-completions
// endpoint.
package testserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// Path is the only route the double answers.
const Path = "/chat/completions"

// ToolCall describes one function call a Reply emits.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON object.
}

// Reply is one scripted model turn.
type Reply struct {
	Reasoning string
	Text      string
	ToolCalls []ToolCall
}

// Server is a started httptest server that answers POST /chat/completions.
type Server struct {
	*httptest.Server

	m        sync.Mutex
	replies  []Reply
	requests [][]byte
}

// New starts a server answering with replies in order.
func New(replies ...Reply) *Server {
	server := &Server{replies: replies}
	server.Server = httptest.NewServer(http.HandlerFunc(server.serve))
	return server
}

// Requests returns a copy of every request body received so far.
func (s *Server) Requests() [][]byte {
	s.m.Lock()
	defer s.m.Unlock()
	return append([][]byte(nil), s.requests...)
}

// next records the request body and returns the reply that answers it.
func (s *Server) next(body []byte) Reply {
	s.m.Lock()
	defer s.m.Unlock()

	s.requests = append(s.requests, body)

	reply := Reply{}
	if len(s.replies) > 0 {
		reply = s.replies[0]
		s.replies = s.replies[1:]
	}

	return reply
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != Path {
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var request struct {
		Stream bool   `json:"stream"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	reply := s.next(body)
	if request.Stream {
		s.writeStream(w, request.Model, reply)
		return
	}
	s.writeCompletion(w, request.Model, reply)
}

type delta struct {
	Role             string         `json:"role,omitempty"`
	Content          *string        `json:"content,omitempty"`
	ReasoningContent *string        `json:"reasoning_content,omitempty"`
	ToolCalls        []toolCallData `json:"tool_calls,omitempty"`
}

type toolCallData struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type choice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage,omitempty"`
}

func (s *Server) writeStream(w http.ResponseWriter, model string, reply Reply) {
	w.Header().Set("Content-Type", "text/event-stream")
	controller := http.NewResponseController(w)

	for _, chunk := range chunksFor(model, reply) {
		data, err := json.Marshal(chunk)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := w.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
			return
		}
		if err := controller.Flush(); err != nil {
			return
		}
	}

	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	_ = controller.Flush()
}

func chunksFor(model string, reply Reply) []chunk {
	newChunk := func(finish *string, d delta, withUsage bool) chunk {
		c := chunk{
			ID:      "chatcmpl-test",
			Object:  "chat.completion.chunk",
			Created: 1,
			Model:   model,
			Choices: []choice{{Index: 0, Delta: d, FinishReason: finish}},
		}
		if withUsage {
			c.Choices = []choice{}
			c.Usage = &usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
		}
		return c
	}

	chunks := []chunk{newChunk(nil, delta{Role: "assistant", Content: new("")}, false)}

	if reply.Reasoning != "" {
		chunks = append(chunks, newChunk(nil, delta{ReasoningContent: new(reply.Reasoning)}, false))
	}
	if reply.Text != "" {
		chunks = append(chunks, newChunk(nil, delta{Content: new(reply.Text)}, false))
	}

	calls := make([]toolCallData, 0, len(reply.ToolCalls))
	for i, call := range reply.ToolCalls {
		data := toolCallData{Index: i, ID: call.ID, Type: "function"}
		data.Function.Name = call.Name
		data.Function.Arguments = call.Arguments
		calls = append(calls, data)
	}
	if len(calls) > 0 {
		chunks = append(chunks, newChunk(nil, delta{ToolCalls: calls}, false))
	}

	finish := "stop"
	if len(calls) > 0 {
		finish = "tool_calls"
	}
	chunks = append(chunks, newChunk(&finish, delta{}, false), newChunk(nil, delta{}, true))

	return chunks
}

func (s *Server) writeCompletion(w http.ResponseWriter, model string, reply Reply) {
	type message struct {
		Role             string         `json:"role"`
		Content          *string        `json:"content"`
		ReasoningContent string         `json:"reasoning_content,omitempty"`
		ToolCalls        []toolCallData `json:"tool_calls,omitempty"`
	}
	type completion struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int     `json:"index"`
			Message      message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage usage `json:"usage"`
	}

	response := completion{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1,
		Model:   model,
		Usage:   usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	}

	turn := message{Role: "assistant", ReasoningContent: reply.Reasoning}
	turn.Content = new(reply.Text)
	for i, call := range reply.ToolCalls {
		data := toolCallData{Index: i, ID: call.ID, Type: "function"}
		data.Function.Name = call.Name
		data.Function.Arguments = call.Arguments
		turn.ToolCalls = append(turn.ToolCalls, data)
	}

	reason := "stop"
	if len(turn.ToolCalls) > 0 {
		reason = "tool_calls"
	}

	response.Choices = append(response.Choices, struct {
		Index        int     `json:"index"`
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	}{Index: 0, Message: turn, FinishReason: reason})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
