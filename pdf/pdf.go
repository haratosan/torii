package pdf

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ToImages converts PDF bytes to PNG images using pdftoppm.
// Returns one PNG per page, up to maxPages.
func findPdftoppm() (string, error) {
	if path, err := exec.LookPath("pdftoppm"); err == nil {
		return path, nil
	}
	// launchd/systemd may not have Homebrew in PATH
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/home/linuxbrew/.linuxbrew/bin"} {
		path := filepath.Join(dir, "pdftoppm")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("pdftoppm not found — install poppler (e.g. 'brew install poppler' or 'apt install poppler-utils')")
}

func ToImages(pdfData []byte, maxPages int) ([][]byte, error) {
	pdftoppm, err := findPdftoppm()
	if err != nil {
		return nil, err
	}

	tmpDir, err := os.MkdirTemp("", "torii-pdf-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	pdfPath := filepath.Join(tmpDir, "input.pdf")
	if err := os.WriteFile(pdfPath, pdfData, 0600); err != nil {
		return nil, fmt.Errorf("write temp pdf: %w", err)
	}

	outPrefix := filepath.Join(tmpDir, "page")
	args := []string{"-png", "-r", "200"}
	if maxPages > 0 {
		args = append(args, "-l", fmt.Sprintf("%d", maxPages))
	}
	args = append(args, pdfPath, outPrefix)

	cmd := exec.Command(pdftoppm, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdftoppm failed: %w — %s", err, stderr.String())
	}

	// Collect output PNGs in sorted order
	matches, err := filepath.Glob(outPrefix + "*.png")
	if err != nil {
		return nil, fmt.Errorf("glob png files: %w", err)
	}
	sort.Strings(matches)

	var pages [][]byte
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read page image: %w", err)
		}
		pages = append(pages, data)
	}

	if len(pages) == 0 {
		return nil, fmt.Errorf("pdftoppm produced no output pages")
	}

	return pages, nil
}

// ExtractText uses an Ollama vision model to OCR text from page images.
func ExtractText(ctx context.Context, ollamaHost, visionModel string, pages [][]byte) (string, error) {
	var parts []string

	for i, page := range pages {
		text, err := ocrPage(ctx, ollamaHost, visionModel, page)
		if err != nil {
			return "", fmt.Errorf("page %d: %w", i+1, err)
		}
		parts = append(parts, text)
	}

	return strings.Join(parts, "\n\n---\n\n"), nil
}

func ocrPage(ctx context.Context, ollamaHost, model string, imageData []byte) (string, error) {
	b64 := base64.StdEncoding.EncodeToString(imageData)

	reqBody := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": "Extract all text from this image exactly as written. Preserve paragraph structure. Output only the extracted text, nothing else.",
				"images":  []string{b64},
			},
		},
		"stream": false,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", ollamaHost+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody bytes.Buffer
		errBody.ReadFrom(resp.Body)
		return "", fmt.Errorf("ollama returned %d: %s", resp.StatusCode, errBody.String())
	}

	var result struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	return strings.TrimSpace(result.Message.Content), nil
}
