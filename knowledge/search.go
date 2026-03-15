package knowledge

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/haratosan/torii/store"
)

// SearchResult represents a matching chunk from vector search.
type SearchResult struct {
	DocumentID    int64
	DocumentTitle string
	Content       string
	Score         float64
}

// KnowledgeStore provides document storage and semantic search.
type KnowledgeStore struct {
	db        *store.Store
	embedder  Embedder
	chunkSize int
	overlap   int
}

// NewKnowledgeStore creates a new knowledge store.
func NewKnowledgeStore(db *store.Store, embedder Embedder, chunkSize int, overlap int) *KnowledgeStore {
	return &KnowledgeStore{
		db:        db,
		embedder:  embedder,
		chunkSize: chunkSize,
		overlap:   overlap,
	}
}

// Add stores a document: chunks the content, generates embeddings, and persists everything.
func (k *KnowledgeStore) Add(ctx context.Context, chatID string, title string, content string) (int64, error) {
	docID, err := k.db.CreateKBDocument(chatID, title, content)
	if err != nil {
		return 0, fmt.Errorf("create document: %w", err)
	}

	chunks := Chunk(content, k.chunkSize, k.overlap)
	if len(chunks) == 0 {
		return docID, nil
	}

	embeddings, err := k.embedder.EmbedBatch(ctx, chunks)
	if err != nil {
		// Clean up the document if embedding fails
		k.db.DeleteKBDocument(chatID, docID)
		return 0, fmt.Errorf("embed chunks: %w", err)
	}

	for i, chunk := range chunks {
		if i >= len(embeddings) {
			break
		}
		blob := float32sToBytes(embeddings[i])
		if err := k.db.CreateKBChunk(docID, chatID, chunk, blob); err != nil {
			return 0, fmt.Errorf("store chunk: %w", err)
		}
	}

	return docID, nil
}

// Search performs semantic search across the chat's knowledge base.
func (k *KnowledgeStore) Search(ctx context.Context, chatID string, query string, topK int) ([]SearchResult, error) {
	queryEmb, err := k.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	chunks, err := k.db.ListKBChunks(chatID)
	if err != nil {
		return nil, fmt.Errorf("load chunks: %w", err)
	}

	if len(chunks) == 0 {
		return nil, nil
	}

	type scored struct {
		chunk store.KBChunk
		score float64
	}

	var results []scored
	for _, c := range chunks {
		emb := bytesToFloat32s(c.Embedding)
		if len(emb) == 0 {
			continue
		}
		score := cosineSimilarity(queryEmb, emb)
		results = append(results, scored{chunk: c, score: score})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if topK > len(results) {
		topK = len(results)
	}

	// Look up document titles
	var searchResults []SearchResult
	for _, r := range results[:topK] {
		title, _ := k.db.GetKBDocumentTitle(r.chunk.DocumentID)
		searchResults = append(searchResults, SearchResult{
			DocumentID:    r.chunk.DocumentID,
			DocumentTitle: title,
			Content:       r.chunk.Content,
			Score:         r.score,
		})
	}

	return searchResults, nil
}

// List returns all documents in the chat's knowledge base.
func (k *KnowledgeStore) List(chatID string) ([]store.KBDocument, error) {
	return k.db.ListKBDocuments(chatID)
}

// Delete removes a document and its chunks from the knowledge base.
func (k *KnowledgeStore) Delete(chatID string, docID int64) error {
	return k.db.DeleteKBDocument(chatID, docID)
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

func float32sToBytes(fs []float32) []byte {
	buf := make([]byte, len(fs)*4)
	for i, f := range fs {
		bits := math.Float32bits(f)
		buf[i*4] = byte(bits)
		buf[i*4+1] = byte(bits >> 8)
		buf[i*4+2] = byte(bits >> 16)
		buf[i*4+3] = byte(bits >> 24)
	}
	return buf
}

func bytesToFloat32s(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	fs := make([]float32, len(b)/4)
	for i := range fs {
		bits := uint32(b[i*4]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		fs[i] = math.Float32frombits(bits)
	}
	return fs
}
