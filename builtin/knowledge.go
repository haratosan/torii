package builtin

import (
	"context"
	"encoding/json"
	"fmt"
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
			Description: "Manage the chat's knowledge base for semantic search. Actions: add (store a document), search (find relevant content), list (show all documents), delete (remove a document). Use this to store and retrieve information that should be searchable by meaning.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"add", "search", "list", "delete"},
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
						"description": "Document ID (for delete action)",
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
				for _, d := range docs {
					contentPreview := d.Content
					if len(contentPreview) > 100 {
						contentPreview = contentPreview[:100] + "..."
					}
					sb.WriteString(fmt.Sprintf("ID: %d | %s | %s\n", d.ID, d.Title, contentPreview))
				}
				return &extension.ExtResponse{Output: sb.String()}, nil

			case "delete":
				if args.ID <= 0 {
					return &extension.ExtResponse{Error: "id is required for delete action"}, nil
				}
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
