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
			Description: "Manage the chat's knowledge base. Actions: add (store a document), search (semantic lookup — returns scored matches AND a footer listing ALL available documents by id+title), list (browse available documents — returns IDs + titles only, NOT content), get (read the FULL content of one document by id — always use this after list or search to actually read a document), reembed (re-chunk and re-embed ALL documents using the current embedding model — run after switching embedding_model in config), delete (IRREVERSIBLY remove a document — only when the user explicitly asks to delete). Flow: call `search`; if the top scored results don't obviously answer the question, pick a promising title from the footer and call `get` with its id.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"add", "search", "list", "get", "reembed", "delete"},
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
					topK = 10
				}
				results, err := ks.Search(ctx, req.ChatID, args.Query, topK)
				if err != nil {
					return &extension.ExtResponse{Error: fmt.Sprintf("search failed: %s", err)}, nil
				}

				var sb strings.Builder
				if len(results) == 0 {
					sb.WriteString("No scored matches.\n")
				} else {
					for i, r := range results {
						sb.WriteString(fmt.Sprintf("[%d] (doc: %s, score: %.3f)\n%s\n\n", i+1, r.DocumentTitle, r.Score, r.Content))
					}
				}

				// Always append a full document index so the LLM can fall back
				// to `get` when scored matches miss the mark (e.g. weak
				// multilingual embedding ranking).
				if docs, err := ks.List(req.ChatID); err == nil && len(docs) > 0 {
					sb.WriteString(fmt.Sprintf("Available documents (%d total) — use `get` with id for full content:\n", len(docs)))
					const maxFooterDocs = 50
					for i, d := range docs {
						if i >= maxFooterDocs {
							sb.WriteString(fmt.Sprintf("- … and %d more (use `list`)\n", len(docs)-maxFooterDocs))
							break
						}
						sb.WriteString(fmt.Sprintf("- %d: %s (%d chars)\n", d.ID, d.Title, len(d.Content)))
					}
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

			case "reembed":
				stats, err := ks.Reembed(ctx, req.ChatID)
				if err != nil {
					return &extension.ExtResponse{Error: fmt.Sprintf("reembed failed after %d docs: %s", stats.Documents, err)}, nil
				}
				slog.Info("knowledge reembed", "chat_id", req.ChatID, "documents", stats.Documents, "chunks", stats.Chunks)
				return &extension.ExtResponse{
					Output: fmt.Sprintf("Re-embedded %d documents, %d chunks using current embedding model.", stats.Documents, stats.Chunks),
					Data:   map[string]any{"documents": stats.Documents, "chunks": stats.Chunks},
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
