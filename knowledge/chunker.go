package knowledge

import (
	"strings"
	"unicode"
)

// Chunk splits text into approximately chunkSize-token segments with overlap.
// Splits prefer sentence/paragraph boundaries.
func Chunk(text string, chunkSize int, overlap int) []string {
	// Estimate tokens as words / 0.75
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	// Convert token counts to word counts
	wordsPerChunk := int(float64(chunkSize) * 0.75)
	wordsOverlap := int(float64(overlap) * 0.75)

	if wordsPerChunk < 1 {
		wordsPerChunk = 1
	}
	if wordsOverlap >= wordsPerChunk {
		wordsOverlap = wordsPerChunk / 4
	}

	// If text fits in one chunk, return as-is
	if len(words) <= wordsPerChunk {
		return []string{strings.TrimSpace(text)}
	}

	// Split into sentences first
	sentences := splitSentences(text)
	if len(sentences) <= 1 {
		// Fall back to word-based chunking
		return chunkByWords(words, wordsPerChunk, wordsOverlap)
	}

	// Group sentences into chunks
	var chunks []string
	var current []string
	currentWords := 0

	for _, sent := range sentences {
		sentWords := len(strings.Fields(sent))
		if currentWords+sentWords > wordsPerChunk && currentWords > 0 {
			chunks = append(chunks, strings.TrimSpace(strings.Join(current, " ")))

			// Keep overlap sentences
			overlapWords := 0
			overlapStart := len(current)
			for i := len(current) - 1; i >= 0; i-- {
				w := len(strings.Fields(current[i]))
				if overlapWords+w > wordsOverlap {
					break
				}
				overlapWords += w
				overlapStart = i
			}
			current = current[overlapStart:]
			currentWords = overlapWords
		}
		current = append(current, sent)
		currentWords += sentWords
	}

	if len(current) > 0 {
		chunks = append(chunks, strings.TrimSpace(strings.Join(current, " ")))
	}

	return chunks
}

func splitSentences(text string) []string {
	var sentences []string
	var current strings.Builder

	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		current.WriteRune(runes[i])

		// Check for sentence-ending punctuation followed by space or end
		if runes[i] == '.' || runes[i] == '!' || runes[i] == '?' || runes[i] == '\n' {
			if i+1 >= len(runes) || unicode.IsSpace(runes[i+1]) || unicode.IsUpper(runes[i+1]) {
				s := strings.TrimSpace(current.String())
				if s != "" {
					sentences = append(sentences, s)
				}
				current.Reset()
			}
		}
	}

	if s := strings.TrimSpace(current.String()); s != "" {
		sentences = append(sentences, s)
	}

	return sentences
}

func chunkByWords(words []string, chunkSize int, overlap int) []string {
	var chunks []string
	for i := 0; i < len(words); i += chunkSize - overlap {
		end := i + chunkSize
		if end > len(words) {
			end = len(words)
		}
		chunks = append(chunks, strings.Join(words[i:end], " "))
		if end == len(words) {
			break
		}
	}
	return chunks
}
