package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/knowledge"
)

type knowledgeArgs struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	Content string `json:"content"`
	Query  string `json:"query"`
	TopK   int    `json:"top_k"`
	ID     int64  `json:"id"`
}

func NewKnowledgeTool(ks *knowledge.KnowledgeStore) *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "knowledge",
			Description: "Manage the chat's knowledge base. Actions: add (store a document), search (semantic lookup — find content by meaning), list (browse available documents — returns IDs + titles only, NOT content), get (read the FULL content of one document by id — always use this after list or search to actually read a document), delete (IRREVERSIBLY remove a document — only when the user explicitly asks to delete). Prefer `search` first; if results are weak, `list` then `get` by id.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"add", "search", "list", "get", "delete"},
						"description": "The action to perform",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Document title (for add action)",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Document content to store (for add action)",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "Search query (for search action)",
					},
					"top_k": map[string]any{
						"type":        "integer",
						"description": "Number of results to return (for search action, default 5)",
					},
					"id": map[string]any{
						"type":        "integer",
						"description": "Document ID (for get and delete actions)",
					},
				},
				"required": []any{"action"},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			var args knowledgeArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			switch args.Action {
			case "add":
				if args.Title == "" || args.Content == "" {
					return &extension.ExtResponse{Error: "title and content are required for add action"}, nil
				}
				docID, err := ks.Add(ctx, req.ChatID, args.Title, args.Content)
				if err != nil {
					return &extension.ExtResponse{Error: fmt.Sprintf("failed to add document: %s", err)}, nil
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("Document '%s' added (ID: %d).", args.Title, docID)}, nil

			case "search":
				if args.Query == "" {
					return &extension.ExtResponse{Error: "query is required for search action"}, nil
				}
				topK := args.TopK
				if topK <= 0 {
					topK = 5
				}
				results, err := ks.Search(ctx, req.ChatID, args.Query, topK)
				if err != nil {
					return &extension.ExtResponse{Error: fmt.Sprintf("search failed: %s", err)}, nil
				}
				if len(results) == 0 {
					return &extension.ExtResponse{Output: "No results found."}, nil
				}

				var sb strings.Builder
				for i, r := range results {
					sb.WriteString(fmt.Sprintf("[%d] (doc: %s, score: %.3f)\n%s\n\n", i+1, r.DocumentTitle, r.Score, r.Content))
				}
				return &extension.ExtResponse{Output: sb.String()}, nil

			case "list":
				docs, err := ks.List(req.ChatID)
				if err != nil {
					return &extension.ExtResponse{Error: fmt.Sprintf("list failed: %s", err)}, nil
				}
				if len(docs) == 0 {
					return &extension.ExtResponse{Output: "No documents in knowledge base."}, nil
				}

				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("%d document(s). Use `get` with an id to read full content.\n", len(docs)))
				for _, d := range docs {
					sb.WriteString(fmt.Sprintf("- ID: %d | %s (%d chars)\n", d.ID, d.Title, len(d.Content)))
				}
				return &extension.ExtResponse{Output: sb.String()}, nil

			case "get":
				if args.ID <= 0 {
					return &extension.ExtResponse{Error: "id is required for get action"}, nil
				}
				doc, err := ks.Get(req.ChatID, args.ID)
				if err != nil {
					return &extension.ExtResponse{Error: fmt.Sprintf("get failed: %s", err)}, nil
				}
				return &extension.ExtResponse{
					Output: fmt.Sprintf("Title: %s\n\n%s", doc.Title, doc.Content),
					Data:   map[string]any{"id": doc.ID, "title": doc.Title, "length": len(doc.Content)},
				}, nil

			case "delete":
				if args.ID <= 0 {
					return &extension.ExtResponse{Error: "id is required for delete action"}, nil
				}
				slog.Warn("knowledge delete",
					"doc_id", args.ID,
					"chat_id", req.ChatID,
					"user_id", req.UserID,
				)
				if err := ks.Delete(req.ChatID, args.ID); err != nil {
					return &extension.ExtResponse{Error: fmt.Sprintf("delete failed: %s", err)}, nil
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("Document %d deleted.", args.ID)}, nil

			default:
				return &extension.ExtResponse{Error: fmt.Sprintf("unknown action: %s", args.Action)}, nil
			}
		},
	}
}
